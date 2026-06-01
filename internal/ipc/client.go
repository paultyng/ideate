package ipc

import (
	"context"
	"fmt"
	"net"
	"net/http"

	"connectrpc.com/connect"

	ipcv1 "github.com/paultyng/ideate/internal/gen/ideate/ipc/v1"
	"github.com/paultyng/ideate/internal/gen/ideate/ipc/v1/ipcv1connect"
)

// Client wraps the generated Connect client for CLI subcommands to call
// into the running Ideate app over Unix domain socket IPC.
type Client struct {
	rpc        ipcv1connect.IdeateServiceClient
	socketPath string
}

// NewClient creates a Client that connects to the default socket path.
func NewClient() (*Client, error) {
	sockPath, err := SocketPath()
	if err != nil {
		return nil, fmt.Errorf("resolving socket path: %w", err)
	}
	return newClient(sockPath), nil
}

// newClient creates a client bound to a specific socket path (used by tests).
func newClient(socketPath string) *Client {
	httpClient := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socketPath)
			},
		},
	}

	return &Client{
		rpc:        ipcv1connect.NewIdeateServiceClient(httpClient, "http://ideate"),
		socketPath: socketPath,
	}
}

// OpenReviewArgs holds parameters for OpenReview.
type OpenReviewArgs struct {
	PR       string // GitHub PR reference (owner/repo#number)
	Repo     string // local git repo path
	Base     string // base ref
	Head     string // head ref
	ReviewID string // optional review ID (enables comment UI)
}

// OpenReview tells the running app to navigate to the review view.
func (c *Client) OpenReview(ctx context.Context, args OpenReviewArgs) error {
	_, err := c.rpc.OpenReview(ctx, connect.NewRequest(&ipcv1.OpenReviewRequest{
		Pr:       args.PR,
		Repo:     args.Repo,
		Base:     args.Base,
		Head:     args.Head,
		ReviewId: args.ReviewID,
	}))
	if err != nil {
		return c.wrapConnErr(err)
	}
	return nil
}

// GetStatus returns the current status of the running app.
func (c *Client) GetStatus(ctx context.Context) (*ipcv1.GetStatusResponse, error) {
	resp, err := c.rpc.GetStatus(ctx, connect.NewRequest(&ipcv1.GetStatusRequest{}))
	if err != nil {
		return nil, c.wrapConnErr(err)
	}
	return resp.Msg, nil
}

func (c *Client) wrapConnErr(err error) error {
	// Check if the underlying error is a connection refusal.
	if connectErr, ok := err.(*connect.Error); ok {
		if connectErr.Code() == connect.CodeUnavailable {
			return fmt.Errorf("ideate is not running (could not connect to %s)", c.socketPath)
		}
	}
	return err
}
