package kube

import (
	"context"
	"fmt"
	"time"

	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/rand"
)

// RecordExecEvent writes a Kubernetes Event on the pod noting who opened a
// shell, so `kubectl describe pod` shows the access. Best-effort: a Forbidden
// (no events/create) is swallowed by the caller.
func (c *Clients) RecordExecEvent(ctx context.Context, ns, pod, container, user string) error {
	p, err := c.Typed.CoreV1().Pods(ns).Get(ctx, pod, metav1.GetOptions{})
	if err != nil {
		return err
	}
	now := metav1.NewTime(time.Now())
	ev := &v1.Event{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s.stepshell.%s", pod, rand.String(8)),
			Namespace: ns,
		},
		InvolvedObject: v1.ObjectReference{
			Kind:       "Pod",
			Namespace:  ns,
			Name:       pod,
			UID:        p.UID,
			APIVersion: "v1",
			FieldPath:  fmt.Sprintf("spec.containers{%s}", container),
		},
		Reason:         "StepshellExec",
		Message:        fmt.Sprintf("%s opened a shell into container %q via stepshell", user, container),
		Type:           v1.EventTypeNormal,
		Source:         v1.EventSource{Component: "stepshell"},
		FirstTimestamp: now,
		LastTimestamp:  now,
		Count:          1,
	}
	_, err = c.Typed.CoreV1().Events(ns).Create(ctx, ev, metav1.CreateOptions{})
	if apierrors.IsForbidden(err) {
		return nil // no rights to write events; not fatal
	}
	return err
}
