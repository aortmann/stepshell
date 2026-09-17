package kube

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	utilexec "k8s.io/client-go/util/exec"
)

// ExecStreams carries the I/O and resize source for an exec session.
type ExecStreams struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	TTY    bool
	Resize remotecommand.TerminalSizeQueue
}

// ExecRequest describes a command to run in a container.
type ExecRequest struct {
	Namespace string
	Pod       string
	Container string
	Command   []string
	TTY       bool
	Streams   ExecStreams
}

// Exec runs cmd in a pod container, streaming through req.Streams. It uses the
// WebSocket executor with a fall back to SPDY for older API servers. The
// returned exit code is best-effort: it is set when the API server reports one.
func (c *Clients) Exec(ctx context.Context, req ExecRequest) (exitCode int, err error) {
	execURL := c.Typed.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(req.Pod).
		Namespace(req.Namespace).
		SubResource("exec").
		VersionedParams(&v1.PodExecOptions{
			Container: req.Container,
			Command:   req.Command,
			Stdin:     req.Streams.Stdin != nil,
			Stdout:    req.Streams.Stdout != nil,
			Stderr:    req.Streams.Stderr != nil && !req.TTY,
			TTY:       req.TTY,
		}, scheme.ParameterCodec).URL()

	exec, err := newFallbackExecutor(c.RESTConf, execURL)
	if err != nil {
		return 0, fmt.Errorf("building executor: %w", err)
	}

	err = exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdin:             req.Streams.Stdin,
		Stdout:            req.Streams.Stdout,
		Stderr:            req.Streams.Stderr,
		Tty:               req.TTY,
		TerminalSizeQueue: req.Streams.Resize,
	})
	if err != nil {
		var codeErr utilexec.CodeExitError
		if errors.As(err, &codeErr) {
			return codeErr.Code, nil
		}
		return 0, err
	}
	return 0, nil
}

// newFallbackExecutor builds a WebSocket executor that falls back to SPDY when
// the initial websocket stream fails on an older API server.
func newFallbackExecutor(cfg *rest.Config, u *url.URL) (remotecommand.Executor, error) {
	ws, err := remotecommand.NewWebSocketExecutor(cfg, "GET", u.String())
	if err != nil {
		return nil, err
	}
	spdy, err := remotecommand.NewSPDYExecutor(cfg, "POST", u)
	if err != nil {
		return nil, err
	}
	return remotecommand.NewFallbackExecutor(ws, spdy, httpstream.IsUpgradeFailure)
}
