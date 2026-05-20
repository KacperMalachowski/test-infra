// Command poc-484 is a standalone PoC test tool for issue #484.
//
// It verifies that an Azure DevOps pipeline can be triggered using an Azure AD
// Bearer token instead of a Personal Access Token (PAT).
//
// This is the Option B variant: the caller acquires the Bearer token externally
// (e.g. via curl against the Azure AD token endpoint) and passes it in via flag:
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
	"time"

	adov7 "github.com/microsoft/azure-devops-go-api/azuredevops/v7"
	"github.com/microsoft/azure-devops-go-api/azuredevops/v7/pipelines"
)

type options struct {
	// ADO target
	adoOrgURL   string
	adoProject  string
	adoPipeline int

	// Option B — pre-acquired token passed in
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
	flag.StringVar(&o.azureAccessTokenType, "azure-access-token-type", "pat", "Token type — 'pat' or 'bearer'")

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

	// --- resolve token ---
	token, tokenType, err := resolveToken(ctx, o)
	if err != nil {
		return err
	}
	fmt.Printf("Token resolved (type: %s). Connecting to %s...\n", tokenType, o.adoOrgURL)

	// --- build ADO connection ---
	conn := newConnection(o.adoOrgURL, token, tokenType)

	// --- trigger pipeline ---
	client := pipelines.NewClient(ctx, conn)

	fmt.Printf("Triggering pipeline %d in project %q...\n", o.adoPipeline, o.adoProject)
	run, err := client.RunPipeline(ctx, pipelines.RunPipelineArgs{
		Project:       &o.adoProject,
		PipelineId:    &o.adoPipeline,
		RunParameters: &pipelines.RunPipelineParameters{
			// No template parameters needed for the dummy pipeline.
		},
	})
	if err != nil {
		return fmt.Errorf("failed to trigger pipeline: %w", err)
	}

	fmt.Printf("Pipeline run created: ID=%d  URL=%s\n", *run.Id, buildRunURL(o.adoOrgURL, o.adoProject, *run.Id))

	// --- poll for result ---
	return waitForResult(ctx, client, o, *run.Id)
}

// resolveToken returns the raw access token and its type ("pat" or "bearer").
func resolveToken(_ context.Context, o options) (token, tokenType string, err error) {
	// Option B — pre-acquired token provided via flag.
	if o.azureAccessToken != "" {
		fmt.Printf("Option B: using pre-acquired token (type: %s).\n", o.azureAccessTokenType)
		return o.azureAccessToken, o.azureAccessTokenType, nil
	}

	// Classic PAT fallback via environment variable.
	if pat, ok := os.LookupEnv("ADO_PAT"); ok {
		fmt.Println("Fallback: using ADO_PAT environment variable.")
		return pat, "pat", nil
	}

	return "", "", fmt.Errorf("no credentials provided: use --azure-access-token (with --azure-access-token-type=bearer for AAD tokens) or set ADO_PAT environment variable")
}

// newConnection builds an ADO Connection for the given token type.
func newConnection(orgURL, token, tokenType string) *adov7.Connection {
	if tokenType == "bearer" {
		return &adov7.Connection{
			AuthorizationString:     "Bearer " + token,
			BaseUrl:                 orgURL,
			SuppressFedAuthRedirect: true,
		}
	}
	return adov7.NewPatConnection(orgURL, token)
}

// waitForResult polls the pipeline run until it reaches a terminal state or the timeout expires.
func waitForResult(ctx context.Context, client pipelines.Client, o options, runID int) error {
	deadline := time.Now().Add(o.timeout)
	for {
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s waiting for pipeline run %d to complete", o.timeout, runID)
		}

		run, err := client.GetRun(ctx, pipelines.GetRunArgs{
			Project:    &o.adoProject,
			PipelineId: &o.adoPipeline,
			RunId:      &runID,
		})
		if err != nil {
			return fmt.Errorf("failed to get pipeline run status: %w", err)
		}

		state := string(*run.State)
		fmt.Printf("  run %d state: %s\n", runID, state)

		if *run.State == pipelines.RunStateValues.Completed {
			result := string(*run.Result)
			fmt.Printf("Pipeline run %d completed with result: %s\n", runID, result)
			if *run.Result != pipelines.RunResultValues.Succeeded {
				return fmt.Errorf("pipeline run did not succeed: %s", result)
			}
			return nil
		}

		time.Sleep(o.pollInterval)
	}
}

// buildRunURL constructs a human-readable ADO pipeline run URL for logging.
func buildRunURL(orgURL, project string, runID int) string {
	return fmt.Sprintf("%s/%s/_build/results?buildId=%d", orgURL, project, runID)
}
