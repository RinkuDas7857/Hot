package sshd

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/pires/go-proxyproto"
	"golang.org/x/crypto/ssh"

	"gitlab.com/gitlab-org/gitlab-shell/internal/config"
	"gitlab.com/gitlab-org/gitlab-shell/internal/gitlabnet/authorizedkeys"

	"gitlab.com/gitlab-org/labkit/correlation"
	"gitlab.com/gitlab-org/labkit/log"
)

type status int

const (
	StatusStarting status = iota
	StatusReady
	StatusOnShutdown
	StatusClosed
	ProxyHeaderTimeout = 90 * time.Second
)

type Server struct {
	Config *config.Config

	status               status
	statusMu             sync.Mutex
	wg                   sync.WaitGroup
	listener             net.Listener
	hostKeys             []ssh.Signer
	authorizedKeysClient *authorizedkeys.Client
}

func NewServer(cfg *config.Config) (*Server, error) {
	authorizedKeysClient, err := authorizedkeys.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize GitLab client: %w", err)
	}

	var hostKeys []ssh.Signer
	for _, filename := range cfg.Server.HostKeyFiles {
		keyRaw, err := os.ReadFile(filename)
		if err != nil {
			log.WithError(err).Warnf("Failed to read host key %v", filename)
			continue
		}
		key, err := ssh.ParsePrivateKey(keyRaw)
		if err != nil {
			log.WithError(err).Warnf("Failed to parse host key %v", filename)
			continue
		}

		hostKeys = append(hostKeys, key)
	}
	if len(hostKeys) == 0 {
		return nil, fmt.Errorf("No host keys could be loaded, aborting")
	}

	return &Server{Config: cfg, authorizedKeysClient: authorizedKeysClient, hostKeys: hostKeys}, nil
}

func (s *Server) ListenAndServe(ctx context.Context) error {
	if err := s.listen(); err != nil {
		return err
	}
	defer s.listener.Close()

	s.serve(ctx)

	return nil
}

func (s *Server) Shutdown() error {
	if s.listener == nil {
		return nil
	}

	s.changeStatus(StatusOnShutdown)

	return s.listener.Close()
}

func (s *Server) MonitoringServeMux() *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc(s.Config.Server.ReadinessProbe, func(w http.ResponseWriter, r *http.Request) {
		if s.getStatus() == StatusReady {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	})

	mux.HandleFunc(s.Config.Server.LivenessProbe, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	return mux
}

func (s *Server) listen() error {
	sshListener, err := net.Listen("tcp", s.Config.Server.Listen)
	if err != nil {
		return fmt.Errorf("failed to listen for connection: %w", err)
	}

	if s.Config.Server.ProxyProtocol {
		sshListener = &proxyproto.Listener{
			Listener:          sshListener,
			ReadHeaderTimeout: ProxyHeaderTimeout,
		}

		log.Info("Proxy protocol is enabled")
	}

	log.WithFields(log.Fields{"tcp_address": sshListener.Addr().String()}).Info("Listening for SSH connections")

	s.listener = sshListener

	return nil
}

func (s *Server) serve(ctx context.Context) {
	s.changeStatus(StatusReady)

	for {
		nconn, err := s.listener.Accept()
		if err != nil {
			if s.getStatus() == StatusOnShutdown {
				break
			}

			log.WithError(err).Warn("Failed to accept connection")
			continue
		}

		s.wg.Add(1)
		go s.handleConn(ctx, nconn)
	}

	s.wg.Wait()

	s.changeStatus(StatusClosed)
}

func (s *Server) changeStatus(st status) {
	s.statusMu.Lock()
	s.status = st
	s.statusMu.Unlock()
}

func (s *Server) getStatus() status {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()

	return s.status
}

func (s *Server) serverConfig(ctx context.Context) *ssh.ServerConfig {
	sshCfg := &ssh.ServerConfig{
		PublicKeyCallback: func(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if conn.User() != s.Config.User {
				return nil, errors.New("unknown user")
			}
			if key.Type() == ssh.KeyAlgoDSA {
				return nil, errors.New("DSA is prohibited")
			}
			ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			res, err := s.authorizedKeysClient.GetByKey(ctx, base64.RawStdEncoding.EncodeToString(key.Marshal()))
			if err != nil {
				return nil, err
			}

			return &ssh.Permissions{
				// Record the public key used for authentication.
				Extensions: map[string]string{
					"key-id": strconv.FormatInt(res.Id, 10),
				},
			}, nil
		},
	}

	for _, key := range s.hostKeys {
		sshCfg.AddHostKey(key)
	}

	return sshCfg
}

func (s *Server) handleConn(ctx context.Context, nconn net.Conn) {
	remoteAddr := nconn.RemoteAddr().String()

	defer s.wg.Done()
	defer nconn.Close()

	// Prevent a panic in a single connection from taking out the whole server
	defer func() {
		if err := recover(); err != nil {
			log.WithFields(log.Fields{"recovered_error": err}).Warnf("panic handling session from %s", remoteAddr)
		}
	}()

	ctx, cancel := context.WithCancel(correlation.ContextWithCorrelation(ctx, correlation.SafeRandomID()))
	defer cancel()

	sconn, chans, reqs, err := ssh.NewServerConn(nconn, s.serverConfig(ctx))
	if err != nil {
		log.WithError(err).Info("Failed to initialize SSH connection")
		return
	}

	go ssh.DiscardRequests(reqs)

	conn := newConnection(s.Config.Server.ConcurrentSessionsLimit, remoteAddr)
	conn.handle(ctx, chans, func(ctx context.Context, channel ssh.Channel, requests <-chan *ssh.Request) {
		session := &session{
			cfg:         s.Config,
			channel:     channel,
			gitlabKeyId: sconn.Permissions.Extensions["key-id"],
			remoteAddr:  remoteAddr,
		}

		session.handle(ctx, requests)
	})
}
