package main

import (
	"strings"
	"testing"
	"text/template"

	agenttesting "github.com/codefly-dev/core/agents/testing"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

func TestDeploymentTemplates(t *testing.T) {
	agenttesting.AssertKustomizeTemplates(t, deploymentFS, nil)
}

// TestStatefulSetReadOnlyRootHasWritablePaths guards the invariant that makes a
// readOnlyRootFilesystem container actually runnable: DynamoDB Local is a JVM +
// SQLite process that spills temp files to /tmp and persists its database to
// -dbPath, so both destinations must resolve to a writable mount rather than the
// read-only root. Without this the manifest passes static conformance yet the pod
// fails to persist or crashes on temp-file writes under load.
func TestStatefulSetReadOnlyRootHasWritablePaths(t *testing.T) {
	raw, err := deploymentFS.ReadFile("templates/deployment/kustomize/base/stateful-set.yaml.tmpl")
	require.NoError(t, err)

	tmpl, err := template.New("statefulset").Parse(string(raw))
	require.NoError(t, err)

	var rendered strings.Builder
	err = tmpl.Execute(&rendered, map[string]string{
		"Name":      "example",
		"Namespace": "codefly-test",
		"Image":     "amazon/dynamodb-local:test",
	})
	require.NoError(t, err)

	var sts struct {
		Spec struct {
			Template struct {
				Spec struct {
					Containers []struct {
						Args            []string `json:"args"`
						SecurityContext struct {
							ReadOnlyRootFilesystem bool `json:"readOnlyRootFilesystem"`
						} `json:"securityContext"`
						VolumeMounts []struct {
							MountPath string `json:"mountPath"`
							ReadOnly  bool   `json:"readOnly"`
						} `json:"volumeMounts"`
					} `json:"containers"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(rendered.String()), &sts))

	containers := sts.Spec.Template.Spec.Containers
	require.NotEmpty(t, containers)

	for _, c := range containers {
		if !c.SecurityContext.ReadOnlyRootFilesystem {
			continue
		}

		writableMounts := map[string]bool{}
		for _, m := range c.VolumeMounts {
			if !m.ReadOnly {
				writableMounts[m.MountPath] = true
			}
		}

		require.True(t, writableMounts["/tmp"],
			"readOnlyRootFilesystem container must mount a writable /tmp for JVM/SQLite temp files")

		dbPath := argValue(c.Args, "-dbPath")
		require.NotEmpty(t, dbPath, "expected a -dbPath argument")
		require.True(t, coveredByWritableMount(dbPath, writableMounts),
			"-dbPath %q is not under a writable mount; DynamoDB cannot persist on a read-only root", dbPath)
	}
}

func argValue(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func coveredByWritableMount(path string, writableMounts map[string]bool) bool {
	for mount := range writableMounts {
		if path == mount || strings.HasPrefix(path, mount+"/") {
			return true
		}
	}
	return false
}
