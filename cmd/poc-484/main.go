// Command poc-484 is a standalone PoC test tool for issue #484.
//
// It verifies that an Azure DevOps pipeline can be triggered using an Azure AD
// Service Principal instead of a Personal Access Token (PAT).
//
// This is the Option A variant: the binary acquires the Bearer token internally
// via the client-credentials flow using pkg/azuredevops/auth:
//
//	--azure-sp-tenant-id, --azure-sp-client-id, --azure-sp-client-secret
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

	adoauth "github.com/kyma-project/test-infra/pkg/azuredevops/auth"
	adopipelines "github.com/kyma-project/test-infra/pkg/azuredevops/pipelines"
)

type options struct {
	// ADO target
	adoOrgURL   string
	adoProject  string
	adoPipeline int

	// Option A — SP credentials, binary acquires token via adoauth library
	azureSPTenantID     string
	azureSPClientID     string
	azureSPClientSecret string

	// PAT fallback
	azureAccessToken string

	// behaviour
	pollInterval time.Duration
	timeout      time.Duration
}

func main() {
	o := options{}

	flag.StringVar(&o.adoOrgURL, "ado-org-url", "https://dev.azure.com/hyperspace-pipelines", "Azure DevOps organisation URL")
	flag.StringVar(&o.adoProject, "ado-project", "kyma", "Azure DevOps project name")
	flag.IntVar(&o.adoPipeline, "ado-pipeline-id", 0, "ID of the dummy ADO pipeline to trigger (required)")

	flag.StringVar(&o.azureSPTenantID, "azure-sp-tenant-id", "", "Azure AD tenant ID for Service Principal auth")
	flag.StringVar(&o.azureSPClientID, "azure-sp-client-id", "", "Azure AD application (client) ID for Service Principal auth")
	flag.StringVar(&o.azureSPClientSecret, "azure-sp-client-secret", "", "Azure AD client secret for Service Principal auth")

	flag.StringVar(&o.azureAccessToken, "azure-access-token", "", "PAT fallback token")

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

	client, err := resolveClient(ctx, o)
	if err != nil {
		return err
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

// resolveClient acquires a token via the appropriate method and returns the
// correct ADO pipelines client. All connection logic goes through the library.
func resolveClient(ctx context.Context, o options) (adopipelines.Client, error) {
	// Option A — SP credentials provided: acquire Bearer token via adoauth library.
	if o.azureSPTenantID != "" || o.azureSPClientID != "" || o.azureSPClientSecret != "" {
		fmt.Println("Option A: acquiring Azure AD Bearer token via Service Principal...")
		ts, err := adoauth.NewServicePrincipalTokenSource(ctx, adoauth.ServicePrincipalConfig{
			TenantID:     o.azureSPTenantID,
			ClientID:     o.azureSPClientID,
			ClientSecret: o.azureSPClientSecret,
		})
		if err != nil {
			return nil, fmt.Errorf("cannot create SP token source: %w", err)
		}
		token, err := ts.ADOToken(ctx)
		if err != nil {
			return nil, fmt.Errorf("cannot acquire Azure AD token: %w", err)
		}
		fmt.Println("Azure AD token acquired successfully.")
		return adopipelines.NewClientWithAADToken(o.adoOrgURL, token), nil
	}

	// PAT — explicit flag.
	if o.azureAccessToken != "" {
		fmt.Println("Using explicit PAT token.")
		return adopipelines.NewClient(o.adoOrgURL, o.azureAccessToken), nil
	}

	// PAT — environment variable fallback.
	if pat, ok := os.LookupEnv("ADO_PAT"); ok {
		fmt.Println("Fallback: using ADO_PAT environment variable.")
		return adopipelines.NewClient(o.adoOrgURL, pat), nil
	}

	return nil, fmt.Errorf("no credentials provided: use --azure-sp-* flags for Service Principal auth, " +
		"--azure-access-token for a PAT, or set ADO_PAT environment variable")
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
