package provider

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/lagebj/terraform-provider-cfssl/internal/clients"
	"github.com/lagebj/terraform-provider-cfssl/internal/sdk"
)

func CfsslProvider() *schema.Provider {
	return cfsslProvider(false)
}

func TestCfsslProvider() *schema.Provider {
	return cfsslProvider(true)
}

func cfsslProvider(supportLegacyTestSuite bool) *schema.Provider {
	// avoids this showing up in test output
	debugLog := func(f string, v ...interface{}) {
		if os.Getenv("TF_LOG") == "" {
			return
		}

		if os.Getenv("TF_ACC") != "" {
			return
		}

		log.Printf(f, v...)
	}

	dataSources := make(map[string]*schema.Resource)
	resources := make(map[string]*schema.Resource)

	// first handle the typed services
	for _, service := range SupportedTypedServices() {
		debugLog("[DEBUG] Registering Data Sources for %q..", service.Name())
		for _, ds := range service.DataSources() {
			key := ds.ResourceType()
			if existing := dataSources[key]; existing != nil {
				panic(fmt.Sprintf("An existing Data Source exists for %q", key))
			}

			wrapper := sdk.NewDataSourceWrapper(ds)
			dataSource, err := wrapper.DataSource()
			if err != nil {
				panic(fmt.Errorf("creating Wrapper for Data Source %q: %+v", key, err))
			}

			dataSources[key] = dataSource
		}

		debugLog("[DEBUG] Registering Resources for %q..", service.Name())
		for _, r := range service.Resources() {
			key := r.ResourceType()
			if existing := resources[key]; existing != nil {
				panic(fmt.Sprintf("An existing Resource exists for %q", key))
			}

			wrapper := sdk.NewResourceWrapper(r)
			resource, err := wrapper.Resource()
			if err != nil {
				panic(fmt.Errorf("creating Wrapper for Resource %q: %+v", key, err))
			}
			resources[key] = resource
		}
	}

	// then handle the untyped services
	for _, service := range SupportedUntypedServices() {
		debugLog("[DEBUG] Registering Data Sources for %q..", service.Name())
		for k, v := range service.SupportedDataSources() {
			if existing := dataSources[k]; existing != nil {
				panic(fmt.Sprintf("An existing Data Source exists for %q", k))
			}

			dataSources[k] = v
		}

		debugLog("[DEBUG] Registering Resources for %q..", service.Name())
		for k, v := range service.SupportedResources() {
			if existing := resources[k]; existing != nil {
				panic(fmt.Sprintf("An existing Resource exists for %q", k))
			}

			resources[k] = v
		}
	}

	p := &schema.Provider{
		Schema: map[string]*schema.Schema{
			"api_endpoint_url": {
				Type:        schema.TypeString,
				Optional:    true,
				DefaultFunc: schema.EnvDefaultFunc("CFSSL_API_ENDPOINT_URL", ""),
				Description: "The CFSSL API endpoint that will be used.",
			},
		},

		DataSourcesMap: dataSources,
		ResourcesMap:   resources,
	}

	p.ConfigureContextFunc = providerConfigure(p)

	return p
}

func providerConfigure(p *schema.Provider) schema.ConfigureContextFunc {
	return func(ctx context.Context, d *schema.ResourceData) (interface{}, diag.Diagnostics) {
		builder := &authentication.Builder{
			SubscriptionID:     d.Get("subscription_id").(string),
			ClientID:           d.Get("client_id").(string),
			ClientSecret:       d.Get("client_secret").(string),
			TenantID:           d.Get("tenant_id").(string),
			AuxiliaryTenantIDs: auxTenants,
			Environment:        d.Get("environment").(string),
			MetadataHost:       metadataHost,
			MsiEndpoint:        d.Get("msi_endpoint").(string),
			ClientCertPassword: d.Get("client_certificate_password").(string),
			ClientCertPath:     d.Get("client_certificate_path").(string),

			// Feature Toggles
			SupportsClientCertAuth:         true,
			SupportsClientSecretAuth:       true,
			SupportsManagedServiceIdentity: d.Get("use_msi").(bool),
			SupportsAzureCliToken:          true,
			SupportsAuxiliaryTenants:       len(auxTenants) > 0,

			// Doc Links
			ClientSecretDocsLink: "https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs/guides/service_principal_client_secret",

			// MSAL opt-in
			UseMicrosoftGraph: useMsal,
		}

		config, err := builder.Build()
		if err != nil {
			return nil, diag.Errorf("building AzureRM Client: %s", err)
		}

		terraformVersion := p.TerraformVersion
		if terraformVersion == "" {
			// Terraform 0.12 introduced this field to the protocol
			// We can therefore assume that if it's missing it's 0.10 or 0.11
			terraformVersion = "0.11+compatible"
		}

		skipProviderRegistration := d.Get("skip_provider_registration").(bool)
		clientBuilder := clients.ClientBuilder{
			AuthConfig:                  config,
			SkipProviderRegistration:    skipProviderRegistration,
			TerraformVersion:            terraformVersion,
			PartnerId:                   d.Get("partner_id").(string),
			DisableCorrelationRequestID: d.Get("disable_correlation_request_id").(bool),
			DisableTerraformPartnerID:   d.Get("disable_terraform_partner_id").(bool),
			Features:                    expandFeatures(d.Get("features").([]interface{})),
			StorageUseAzureAD:           d.Get("storage_use_azuread").(bool),
			UseMSAL:                     useMsal,

			// this field is intentionally not exposed in the provider block, since it's only used for
			// platform level tracing
			CustomCorrelationRequestID: os.Getenv("ARM_CORRELATION_REQUEST_ID"),
		}

		//lint:ignore SA1019 SDKv2 migration - staticcheck's own linter directives are currently being ignored under golanci-lint
		stopCtx, ok := schema.StopContext(ctx) //nolint:staticcheck
		if !ok {
			stopCtx = ctx
		}

		client, err := clients.Build(stopCtx, clientBuilder)
		if err != nil {
			return nil, diag.FromErr(err)
		}

		client.StopContext = stopCtx

		return client, nil
	}
}

const resourceProviderRegistrationErrorFmt = `Error ensuring Resource Providers are registered.

Terraform automatically attempts to register the Resource Providers it supports to
ensure it's able to provision resources.

If you don't have permission to register Resource Providers you may wish to use the
"skip_provider_registration" flag in the Provider block to disable this functionality.

Please note that if you opt out of Resource Provider Registration and Terraform tries
to provision a resource from a Resource Provider which is unregistered, then the errors
may appear misleading - for example:

> API version 2019-XX-XX was not found for Microsoft.Foo

Could indicate either that the Resource Provider "Microsoft.Foo" requires registration,
but this could also indicate that this Azure Region doesn't support this API version.

More information on the "skip_provider_registration" flag can be found here:
https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs#skip_provider_registration

Original Error: %s`
