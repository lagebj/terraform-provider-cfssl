package provider

import (
	"github.com/hashicorp/terraform-provider-azurerm/internal/services/policy"
)

//go:generate go run ../tools/generator-services/main.go -path=../../

func SupportedTypedServices() []sdk.TypedServiceRegistration {
	return []sdk.TypedServiceRegistration{
		policy.Registration{},
	}
}

func SupportedUntypedServices() []sdk.UntypedServiceRegistration {
	return []sdk.UntypedServiceRegistration{
		keyvault.Registration{},
	}
}
