package apprise

import (
	"fmt"
	"time"

	"github.com/go-resty/resty/v2"

	"github.com/fusionn-muse/internal/config"
	"github.com/fusionn-muse/pkg/logger"
)

// Client wraps the Apprise API.
type Client struct {
	cfg    config.AppriseConfig
	client *resty.Client
}

// NewClient creates a new Apprise client.
func NewClient(cfg config.AppriseConfig) *Client {
	client := resty.New().
		SetTimeout(30 * time.Second).
		SetRetryCount(2).
		SetRetryWaitTime(1 * time.Second)

	return &Client{
		cfg:    cfg,
		client: client,
	}
}

// NotifyRequest is the request body for Apprise.
type NotifyRequest struct {
	Body  string `json:"body"`
	Title string `json:"title,omitempty"`
	Type  string `json:"type,omitempty"` // info, success, warning, failure
	Tag   string `json:"tag,omitempty"`
}

// Notify sends a notification via Apprise.
func (c *Client) Notify(title, body, notifyType string) error {
	if !c.cfg.Enabled {
		return nil
	}

	tag := c.cfg.Tag
	if tag == "" {
		tag = "all"
	}

	req := NotifyRequest{
		Title: title,
		Body:  body,
		Type:  notifyType,
		Tag:   tag,
	}

	url := fmt.Sprintf("%s/notify/%s", c.cfg.BaseURL, c.cfg.Key)

	resp, err := c.client.R().
		SetHeader("Content-Type", "application/json").
		SetBody(req).
		Post(url)

	if err != nil {
		return fmt.Errorf("apprise request: %w", err)
	}

	if resp.StatusCode() >= 400 {
		return fmt.Errorf("apprise error: %s", resp.String())
	}

	logger.Debugf("🔔 Notification sent: %s", title)
	return nil
}

// NotifySuccess sends a success notification.
func (c *Client) NotifySuccess(title, body string) error {
	return c.Notify(title, body, "success")
}

// NotifyError sends an error notification.
func (c *Client) NotifyError(title, body string) error {
	return c.Notify(title, body, "failure")
}

// NotifyInfo sends an info notification.
func (c *Client) NotifyInfo(title, body string) error {
	return c.Notify(title, body, "info")
}

