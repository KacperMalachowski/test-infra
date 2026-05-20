// Command poc-484 is a standalone PoC test tool for issue #484.
//
// It verifies that an Azure DevOps pipeline can be triggered using an Azure AD
// Bearer token instead of a Personal Access Token (PAT).
//
// This is the Option B variant: the token is acquired externally (e.g. via the
// Azure CLI in the workflow) and passed in via flag:
//
//	--azure-access-token=<bearer-token> --azure-access-token-type=bearer
//
// Falls back to ADO_PAT environment variable for classic PAT auth.
//
// The tool triggers the dummy ADO pipeline specified by --ado-pipeline-id and
// waits for it to complete, printing the final run result.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/microsoft/azure-devops-go-api/azuredevops/v7/pipelines"

	adopipelines "github.com/kyma-project/test-infra/pkg/azuredevops/pipelines"
)

type options struct {
	// ADO target
	adoOrgURL   string
	adoProject  string
	adoPipeline int

	// Token — pre-acquired externally (Bearer or PAT)
	azureAccessToken     string
	azureAccessTokenType string

	// behaviour
	pollInterval time.Duration
	timeout      time.Duration
}

func main() {
	o := options{}

	flag.StringVar(&o.adoOrgURL, "ado-org-url", "https://dev.azure.com/hyperspace-pipelines", "Azure DevOps organisation URL")
	flag.StringVar(&o.adoProject, "ado-project", "kyma", "Azure DevOps project name")
	flag.IntVar(&o.adoPipeline, "ado-pipeline-id", 0, "ID of the dummy ADO pipeline to trigger (required)")

	flag.StringVar(&o.azureAccessToken, "azure-access-token", "", "Pre-acquired token (PAT or Bearer)")
	flag.StringVar(&o.azureAccessTokenType, "azure-access-token-type", "pat", "Token type: 'pat' or 'bearer'")

	flag.DurationVar(&o.pollInterval, "poll-interval", 10*time.Second, "How often to poll for pipeline run status")
	flag.DurationVar(&o.timeout, "timeout", 5*time.Minute, "Maximum time to wait for pipeline run to complete")

	flag.Parse()

	if err := run(o); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
	}
}

func run(o options) error {
	if o.adoPipeline == 0 {
		return fmt.Errorf("--ado-pipeline-id is required")
	}

	ctx := context.Background()

	token, tokenType, err := resolveToken(o)
	if err != nil {
		return err
	}
	fmt.Printf("Token resolved (type: %s). Connecting to %s...\n", tokenType, o.adoOrgURL)

	// Use the library to build the correct ADO client for the token type.
	var client adopipelines.Client
	if tokenType == "bearer" {
		client = adopipelines.NewClientWithAADToken(o.adoOrgURL, token)
	} else {
		client = adopipelines.NewClient(o.adoOrgURL, token)
	}

	fmt.Printf("Triggering pipeline %d in project %q...\n", o.adoPipeline, o.adoProject)
	pipelineRun, err := client.RunPipeline(ctx, pipelines.RunPipelineArgs{
		Project:       &o.adoProject,
		PipelineId:    &o.adoPipeline,
		RunParameters: &pipelines.RunPipelineParameters{
			// No template parameters needed for the dummy pipeline.
		},
	})
	if err != nil {
		return fmt.Errorf("failed to trigger pipeline: %w", err)
	}

	fmt.Printf("Pipeline run created: ID=%d  URL=%s\n", *pipelineRun.Id, buildRunURL(o.adoOrgURL, o.adoProject, *pipelineRun.Id))

	return waitForResult(ctx, client, o, *pipelineRun.Id)
}

// resolveToken returns the raw access token and its type ("pat" or "bearer").
func resolveToken(o options) (token, tokenType string, err error) {
	if o.azureAccessToken != "" {
		fmt.Printf("Using pre-acquired token (type: %s).\n", o.azureAccessTokenType)
		return o.azureAccessToken, o.azureAccessTokenType, nil
	}

	if pat, ok := os.LookupEnv("ADO_PAT"); ok {
		fmt.Println("Fallback: using ADO_PAT environment variable.")
		return pat, "pat", nil
	}

	return "", "", fmt.Errorf("no credentials provided: use --azure-access-token " +
		"(with --azure-access-token-type=bearer for AAD tokens) or set ADO_PAT environment variable")
}

// waitForResult polls the pipeline run until it reaches a terminal state or the timeout expires.
func waitForResult(ctx context.Context, client adopipelines.Client, o options, runID int) error {
	deadline := time.Now().Add(o.timeout)
	for {
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s waiting for pipeline run %d to complete", o.timeout, runID)
		}

		pipelineRun, err := client.GetRun(ctx, pipelines.GetRunArgs{
			Project:    &o.adoProject,
			PipelineId: &o.adoPipeline,
			RunId:      &runID,
		})
		if err != nil {
			return fmt.Errorf("failed to get pipeline run status: %w", err)
		}

		fmt.Printf("  run %d state: %s\n", runID, string(*pipelineRun.State))

		if *pipelineRun.State == pipelines.RunStateValues.Completed {
			result := string(*pipelineRun.Result)
			fmt.Printf("Pipeline run %d completed with result: %s\n", runID, result)
			if *pipelineRun.Result != pipelines.RunResultValues.Succeeded {
				return fmt.Errorf("pipeline run did not succeed: %s", result)
			}
			return nil
		}

		time.Sleep(o.pollInterval)
	}
}

// buildRunURL constructs a human-readable ADO pipeline run URL for logging.
func buildRunURL(orgURL, project string, runID int) string {
	org := orgURL
	if idx := strings.LastIndex(orgURL, "/"); idx >= 0 {
		org = orgURL[idx+1:]
	}
	return fmt.Sprintf("https://dev.azure.com/%s/%s/_build/results?buildId=%d", org, project, runID)
}
