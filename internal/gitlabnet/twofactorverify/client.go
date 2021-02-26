package twofactorverify

import (
	"context"
	"fmt"
	"net/http"

	"gitlab.com/gitlab-org/gitlab-shell/v14/client"
	"gitlab.com/gitlab-org/gitlab-shell/v14/internal/command/commandargs"
	"gitlab.com/gitlab-org/gitlab-shell/v14/internal/config"
	"gitlab.com/gitlab-org/gitlab-shell/v14/internal/gitlabnet"
	"gitlab.com/gitlab-org/gitlab-shell/v14/internal/gitlabnet/discover"
)

type Client struct {
	config *config.Config
	client *client.GitlabNetClient
}

type Response struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

type RequestBody struct {
	KeyId      string `json:"key_id,omitempty"`
	UserId     int64  `json:"user_id,omitempty"`
	OTPAttempt string `json:"otp_attempt"`
}

func NewClient(config *config.Config) (*Client, error) {
	client, err := gitlabnet.GetClient(config)
	if err != nil {
		return nil, fmt.Errorf("Error creating http client: %v", err)
	}

	return &Client{config: config, client: client}, nil
}

func (c *Client) VerifyOTP(ctx context.Context, args *commandargs.Shell, otp string) (bool, string, error) {
	requestBody, err := c.getRequestBody(ctx, args, otp)
	if err != nil {
		return false, "", err
	}

	response, err := c.client.Post(ctx, "/two_factor_manual_otp_check", requestBody)
	if err != nil {
		return false, "", err
	}
	defer response.Body.Close()

	return parse(response)
}

func (c *Client) PushAuth(ctx context.Context, args *commandargs.Shell) (bool, string, error) {
	// enable push auth in internal rest api
	requestBody, err := c.getRequestBody(ctx, args, "")
	if err != nil {
		return false, "", err
	}

	response, err := c.client.Post(ctx, "/two_factor_push_otp_check", requestBody)
	if err != nil {
		return false, "", err
	}
	defer response.Body.Close()

	return parse(response)
}

func parse(hr *http.Response) (bool, string, error) {
	response := &Response{}
	if err := gitlabnet.ParseJSON(hr, response); err != nil {
		return false, "", err
	}

	if !response.Success {
		return false, response.Message, nil
	}

	return true, response.Message, nil
}

func (c *Client) getRequestBody(ctx context.Context, args *commandargs.Shell, otp string) (*RequestBody, error) {
	client, err := discover.NewClient(c.config)

	if err != nil {
		return nil, err
	}

	var requestBody *RequestBody
	if args.GitlabKeyId != "" {
		requestBody = &RequestBody{KeyId: args.GitlabKeyId, OTPAttempt: otp}
	} else {
		userInfo, err := client.GetByCommandArgs(ctx, args)

		if err != nil {
			return nil, err
		}

		requestBody = &RequestBody{UserId: userInfo.UserId, OTPAttempt: otp}
	}

	return requestBody, nil
}
