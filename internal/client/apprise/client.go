package apprise

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
	"go.uber.org/zap"

	"github.com/fusionn-muse/internal/config"
	"github.com/fusionn-muse/pkg/logger"
)

// Client wraps the Apprise API.
type Client struct {
	cfg    config.AppriseConfig
	client *resty.Client
}

// quietRestyLogger swallows Resty's own WARN/ERROR lines. They bypass the
// unified format, and they repeat the request URL, which embeds the Apprise key
// at /notify/<key>.
type quietRestyLogger struct{}

func (quietRestyLogger) Errorf(string, ...interface{}) {}
func (quietRestyLogger) Warnf(string, ...interface{})  {}
func (quietRestyLogger) Debugf(string, ...interface{}) {}

// NewClient creates a new Apprise client.
func NewClient(cfg config.AppriseConfig) *Client {
	client := resty.New().
		SetTimeout(30 * time.Second).
		SetRetryCount(2).
		SetRetryWaitTime(1 * time.Second).
		SetDebug(false).
		SetLogger(quietRestyLogger{})

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
func (c *Client) Notify(ctx context.Context, title, body, notifyType string) error {
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

	endpoint := fmt.Sprintf("%s/notify/%s", c.cfg.BaseURL, c.cfg.Key)

	resp, err := c.client.R().
		SetHeader("Content-Type", "application/json").
		SetBody(req).
		Post(endpoint)

	log := logger.Component("apprise")
	if err != nil {
		// The raw Resty error is a *url.Error carrying the full URL, with the
		// key in its path, so only a key-redacted excerpt may reach DEBUG.
		log.Debug(ctx, "notification_transport_detail", "Notification transport detail",
			logger.Preview(c.redact(err.Error())))
		return fmt.Errorf("apprise request: %w", c.safeCause(err))
	}

	if resp.StatusCode() >= 400 {
		// The remote body is untrusted content: it never becomes part of the
		// error, and only a bounded, redacted excerpt reaches DEBUG.
		log.Debug(ctx, "notification_error_detail", "Notification error detail",
			logger.Preview(c.redact(resp.String())))
		return fmt.Errorf("apprise error: status %d", resp.StatusCode())
	}

	log.Debug(ctx, "notification_sent", "Notification sent", zap.String("title", title))
	return nil
}

// redact replaces the configured key anywhere in text. The key sits in an
// opaque URL path, so no content pattern can recognize it; only the known value
// can be replaced reliably.
func (c *Client) redact(text string) string {
	if c.cfg.Key == "" {
		return text
	}
	return strings.ReplaceAll(text, c.cfg.Key, "***")
}

// safeCause strips the *url.Error wrapper, so the returned error keeps the
// transport cause (timeout, refused, EOF) without the URL and its key.
func (c *Client) safeCause(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		return urlErr.Err
	}
	return err
}

// NotifySuccess sends a success notification.
func (c *Client) NotifySuccess(ctx context.Context, title, body string) error {
	return c.Notify(ctx, title, body, "success")
}

// NotifyError sends an error notification.
func (c *Client) NotifyError(ctx context.Context, title, body string) error {
	return c.Notify(ctx, title, body, "failure")
}

// NotifyInfo sends an info notification.
func (c *Client) NotifyInfo(ctx context.Context, title, body string) error {
	return c.Notify(ctx, title, body, "info")
}
