package objstore

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestNamespaceKey_AzureEffectiveIdentity: an Azure namespace is the
// container at its effective endpoint. The omitted endpoint is the account's
// default blob host, so it and every spelling of that host key alike; a
// different account, container, host, scheme or emulator path does not.
func TestNamespaceKey_AzureEffectiveIdentity(t *testing.T) {
	with := func(extra map[string]string) map[string]string {
		settings := map[string]string{"accountName": "acct", "containerName": "c"}
		for name, value := range extra {
			settings[name] = value
		}
		return settings
	}
	base := namespaceKey("azure", with(nil))

	for _, extra := range []map[string]string{
		{"endpoint": "https://acct.blob.core.windows.net"},
		{"endpoint": "https://ACCT.blob.core.windows.net:443/"},
		{"endpoint": "acct.blob.core.windows.net"},
		{"resourceGroup": "rg"},
	} {
		require.Equal(t, base, namespaceKey("azure", with(extra)), "%v", extra)
	}
	for _, extra := range []map[string]string{
		{"containerName": "other"},
		{"accountName": "other"},
		{"endpoint": "https://other.blob.core.windows.net"},
		{"endpoint": "http://acct.blob.core.windows.net"},
	} {
		require.NotEqual(t, base, namespaceKey("azure", with(extra)), "%v", extra)
	}
	require.NotEqual(t,
		namespaceKey("azure", with(map[string]string{"endpoint": "http://azurite.invalid:10000/acct"})),
		namespaceKey("azure", with(map[string]string{"endpoint": "http://azurite.invalid:10000/other"})))
}

// TestNew_AzureEquivalentEndpointsConcurrentOwners: two owners opening one
// container, one with the endpoint omitted and one naming the account's
// default blob host, race for one owner lock: exactly one opens.
func TestNew_AzureEquivalentEndpointsConcurrentOwners(t *testing.T) {
	requireOneOwnerAcrossSpellings(t, "azure", []map[string]string{
		{"accountName": "acct", "containerName": "c"},
		{"accountName": "acct", "containerName": "c", "endpoint": "https://acct.blob.core.windows.net/"},
	})
}
