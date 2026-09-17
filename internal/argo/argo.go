// Package argo adds Argo Workflows awareness: reading workflows through the
// dynamic client, computing step pod names, and re-running a workflow with
// debug-pause breakpoints injected.
package argo

import (
	"fmt"
	"hash/fnv"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// GVRs for the Argo CRDs, v1alpha1.
var (
	WorkflowGVR = schema.GroupVersionResource{
		Group: "argoproj.io", Version: "v1alpha1", Resource: "workflows",
	}
	WorkflowTemplateGVR = schema.GroupVersionResource{
		Group: "argoproj.io", Version: "v1alpha1", Resource: "workflowtemplates",
	}
	ClusterWorkflowTemplateGVR = schema.GroupVersionResource{
		Group: "argoproj.io", Version: "v1alpha1", Resource: "clusterworkflowtemplates",
	}
)

const (
	labelWorkflow          = "workflows.argoproj.io/workflow"
	annotationNodeID       = "workflows.argoproj.io/node-id"
	annotationPodNameVer   = "workflows.argoproj.io/pod-name-format"
	maxK8sResourceNameLen  = 253
	k8sNamingHashLength    = 10
	maxPrefixLength        = maxK8sResourceNameLen - k8sNamingHashLength
	debugPauseBeforeEnvVar = "ARGO_DEBUG_PAUSE_BEFORE"
	debugPauseAfterEnvVar  = "ARGO_DEBUG_PAUSE_AFTER"
)

// generatePodName reproduces workflow/util.GeneratePodName for the v2 format
// (the default): "<workflow>-<template>-<fnv32a(nodeName)>", trimmed to length,
// with the workflow name returned directly for the root node. It is a faithful
// port of the upstream v4.1.3 implementation so names match for deleted pods.
func generatePodName(workflowName, nodeName, templateName, nodeID string, v2 bool) string {
	if !v2 {
		return nodeID
	}
	if workflowName == nodeName {
		return workflowName
	}
	prefix := workflowName
	if templateName != "" {
		prefix = fmt.Sprintf("%s-%s", prefix, templateName)
	}
	prefix = ensurePrefixLen(prefix)

	h := fnv.New32a()
	_, _ = h.Write([]byte(nodeName))
	return fmt.Sprintf("%s-%v", prefix, h.Sum32())
}

func ensurePrefixLen(prefix string) string {
	if len(prefix) > maxPrefixLength-1 {
		return prefix[0 : maxPrefixLength-1]
	}
	return prefix
}
