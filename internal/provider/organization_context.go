// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

// organizationContextKey is the private state key that records the
// organization a resource lives in. A resource reached through the exchange
// always has the record. A resource reached with the key itself has it when the
// key can read its own organization (organization:read). A state without the
// record belongs to a key that cannot, or was written before the record
// existed, and takes on the organization that the provider or the project
// names now.
const organizationContextKey = "organization_context"

// organizationRecord is the value under organizationContextKey. The ID tells
// a renamed organization from a deleted one, and a provider that now names
// another organization from the one the resource lives in.
type organizationRecord struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type privateStateReader interface {
	GetKey(ctx context.Context, key string) ([]byte, diag.Diagnostics)
}

type privateState interface {
	privateStateReader
	SetKey(ctx context.Context, key string, value []byte) diag.Diagnostics
}

// organizationNamePattern is the instance's rule for organization names. The
// exchange reads the audience <base_url>/<organization> as a path, so a name
// outside the rule, such as "team-a/", would act in another organization.
var organizationNamePattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

const organizationNameRule = "must use lowercase letters, numbers, and internal hyphens (no spaces or uppercase characters)"

func organizationNameValidators() []validator.String {
	return []validator.String{
		stringvalidator.LengthAtLeast(2),
		stringvalidator.RegexMatches(organizationNamePattern, organizationNameRule),
	}
}

func organizationSet(organization types.String) bool {
	return !organization.IsNull() && !organization.IsUnknown() && organization.ValueString() != ""
}

// clientForOrganization returns the client that creates a resource in the
// organization a user configured. A null, unknown, or empty value keeps the
// provider's client, which already acts in the provider organization.
func clientForOrganization(c *logclient.APIClient, organization types.String) *logclient.APIClient {
	if !organizationSet(organization) {
		return c
	}
	return c.ForOrganization(organization.ValueString())
}

// clientForState returns the client that reaches an existing resource.
// organization is the organization that the resource's state names, or null
// for a resource that acts in the provider organization. found is false when
// the organization recorded for the resource was deleted: Read then removes
// the resource from state and Delete succeeds.
//
// The record decides. A resource with a record acts in the organization with
// the recorded ID. A project follows that organization through a rename,
// because its state names the organization only as a copy of the record, and
// the refresh updates the copy. A resource that acts in the provider
// organization fails when the provider names another organization, or the
// old name after a rename, because acting there would read or change another
// organization's resources. A resource without a record acts in the named
// organization, which must exist.
func clientForState(
	ctx context.Context, c *logclient.APIClient, organization types.String, private privateStateReader,
) (client *logclient.APIClient, found bool, diags diag.Diagnostics) {
	record, diags := loadOrganizationRecord(ctx, private)
	if diags.HasError() {
		return c, true, diags
	}

	target := c.Organization()
	if record == nil {
		if target == "" {
			return c, true, nil
		}
		if organizationSet(organization) {
			target = organization.ValueString()
		}
		named, err := findOrganization(ctx, c, true, organizationNamed(target))
		switch {
		case err != nil:
			diags.AddError("Organization lookup failed", err.Error())
		case named == nil:
			diags.AddError("Organization not found", fmt.Sprintf(
				"Organization %q does not exist on this instance. If it was deleted outside Terraform, remove its resources "+
					"from state with terraform state rm. If it was renamed, use the new name.", target))
		}
		return c.ForOrganization(target), true, diags
	}

	var own *logclient.OrganizationRead
	if target == "" {
		own, _ = c.OwnOrganization(ctx)
		if own != nil && own.ID == record.ID {
			return c, true, nil
		}
	}

	current, err := findOrganization(ctx, c, false, func(o logclient.OrganizationRead) bool { return o.ID == record.ID })
	switch {
	case err != nil && target == "" && isListRefusal(err):
		diags.AddError("Cannot confirm the organization of this resource", unconfirmedDetail(record, own))
	case err != nil:
		diags.AddError("Organization lookup failed", err.Error())
	case current == nil:
		return c, false, nil
	case organizationSet(organization) || current.OrganizationName == target:
		return c.ForOrganization(current.OrganizationName), true, nil
	case target == record.Name:
		diags.AddError("Organization renamed", fmt.Sprintf(
			"Organization %q was renamed to %q outside Terraform. Set the provider organization to %q.",
			record.Name, current.OrganizationName, current.OrganizationName))
	default:
		diags.AddError("Resource is in another organization", movedProviderDetail(ctx, c, current.OrganizationName, target))
	}
	return c, true, diags
}

// unconfirmedDetail explains a resource that the provider reached through the
// exchange, now met by a key of its own organization that cannot list the
// instance organizations. own is what the key reports about its organization,
// or nil when it cannot say.
func unconfirmedDetail(record *organizationRecord, own *logclient.OrganizationRead) string {
	if own != nil {
		return fmt.Sprintf("This resource lives in organization %q, but the provider now acts in the organization of its API key, %q. "+
			"Terraform cannot move a resource between organizations. To keep managing it, use a key of organization %q "+
			"or an instance-admin key with the provider organization set to %q. To manage it in organization %q, "+
			"remove it from state with terraform state rm, then create or import it again.",
			record.Name, own.OrganizationName, record.Name, record.Name, own.OrganizationName)
	}
	return fmt.Sprintf("This resource was recorded in organization %q. The provider now acts in the organization of its API key, "+
		"and Terraform cannot confirm that it is %q: the key cannot read its own organization (it needs organization:read) "+
		"or list the instance organizations (it needs instance:admin). Grant the key organization:read, "+
		"or set the provider organization to %q with an instance-admin key. "+
		"To manage the resource with this key anyway, remove it from state with terraform state rm. The key needs "+
		"organization:read, or the provider organization set with an instance-admin key, before it can import the resource again.",
		record.Name, record.Name, record.Name)
}

// isListRefusal reports that the key may not list the instance organizations.
func isListRefusal(err error) bool {
	var apiErr *logclient.APIError
	return errors.As(err, &apiErr) && (apiErr.StatusCode == http.StatusUnauthorized || apiErr.StatusCode == http.StatusForbidden)
}

func movedProviderDetail(ctx context.Context, c *logclient.APIClient, current, target string) string {
	now := "the organization of its API key"
	if target != "" {
		now = fmt.Sprintf("%q", target)
	}
	detail := fmt.Sprintf("This resource lives in organization %q, but the provider now acts in %s. "+
		"Terraform cannot move a resource between organizations. To keep managing it, set the provider "+
		"organization back to %q. To manage it in the new organization, remove it from state with "+
		"terraform state rm, then create or import it again.", current, now, current)
	if target == "" {
		return detail
	}
	if named, err := findOrganization(ctx, c, true, organizationNamed(target)); err == nil && named == nil {
		detail += fmt.Sprintf(" Organization %q does not exist yet. If this configuration renames %q to %q, "+
			"apply the rename alone first with terraform apply -target on the logfire_organization resource. "+
			"Resources then follow the renamed organization.", target, current, target)
	}
	return detail
}

// checkReplacementOrganization refuses to replace a resource with one in an
// organization that does not exist or is not known yet. The replacement deletes
// the old resource first, and the create would then fail. noun names the
// resource in the message. An unknown planned organization is refused because
// it cannot be checked before the delete. A configuration that keeps the old
// name of an organization renamed outside Terraform plans such a replacement,
// because the refresh follows the rename.
func checkReplacementOrganization(ctx context.Context, c *logclient.APIClient, noun string, prior, planned types.String) diag.Diagnostics {
	var diags diag.Diagnostics
	if planned.IsUnknown() {
		diags.AddAttributeError(path.Root("organization"), "Organization not known until apply", fmt.Sprintf(
			"The organization of this %s is not known yet. If it changes, Terraform replaces the %s by deleting it in %q first, "+
				"and it cannot check before that whether the new organization exists. Apply what produces the organization first, "+
				"for example with terraform apply -target, so that the value is known at plan time.", noun, noun, prior.ValueString()))
		return diags
	}
	named, err := findOrganization(ctx, c, true, organizationNamed(planned.ValueString()))
	if err != nil {
		diags.AddAttributeError(path.Root("organization"), "Organization lookup failed", err.Error())
		return diags
	}
	if named == nil {
		diags.AddAttributeError(path.Root("organization"), "Organization not found", fmt.Sprintf(
			"Organization %q does not exist, so replacing the %s would delete it and then fail to create it. "+
				"If the organization was renamed, set organization to its new name: the %s is in %q now. "+
				"If this configuration creates %q or renames an organization to it, apply that alone first "+
				"with terraform apply -target on the logfire_organization resource.",
			planned.ValueString(), noun, noun, prior.ValueString(), planned.ValueString()))
	}
	return diags
}

// checkUnsetOrganization covers a resource whose configuration sets no
// organization. It stays in the organization of its state, so a provider that
// now acts in another organization must not change it there. prior is the
// organization in state.
func checkUnsetOrganization(c *logclient.APIClient, noun string, prior types.String) diag.Diagnostics {
	var diags diag.Diagnostics
	now := c.Organization()
	if now == "" || prior.ValueString() == "" || now == prior.ValueString() {
		return diags
	}
	diags.AddAttributeError(path.Root("organization"), "Resource is in another organization", fmt.Sprintf(
		"This %s lives in organization %q, but it sets no organization and the provider now acts in %q. "+
			"Terraform cannot move a %s between organizations. To keep managing it, set organization = %q on the %s "+
			"or set the provider organization back. To manage it in %q, remove it from state with terraform state rm, "+
			"then create or import it again.", noun, prior.ValueString(), now, noun, prior.ValueString(), noun, now))
	return diags
}

func organizationNamed(name string) func(logclient.OrganizationRead) bool {
	return func(o logclient.OrganizationRead) bool { return o.OrganizationName == name }
}

// saveOrganizationContext records the organization that client acts in. For a
// client of the credential's own organization, that is the organization the
// credential reports for itself. A credential that cannot say, or an instance
// that does not answer, leaves the private state as it is, so Create and Read
// never fail for it.
func saveOrganizationContext(ctx context.Context, private privateState, client *logclient.APIClient) diag.Diagnostics {
	var diags diag.Diagnostics
	var organization *logclient.OrganizationRead
	if name := client.Organization(); name != "" {
		var err error
		organization, err = findOrganization(ctx, client, true, organizationNamed(name))
		if err != nil {
			diags.AddError("Organization lookup failed", err.Error())
			return diags
		}
		if organization == nil {
			diags.AddError("Organization not found", fmt.Sprintf("Organization %q does not exist on this instance.", name))
			return diags
		}
	} else {
		var err error
		organization, err = client.OwnOrganization(ctx)
		if err != nil {
			tflog.Warn(ctx, "could not read the organization of the API key, so the resource keeps no organization record", map[string]any{"error": err.Error()})
			return diags
		}
		if organization == nil {
			return diags
		}
	}
	value, err := json.Marshal(organizationRecord{ID: organization.ID, Name: organization.OrganizationName})
	if err != nil {
		diags.AddError("Record organization failed", err.Error())
		return diags
	}
	return private.SetKey(ctx, organizationContextKey, value)
}

func loadOrganizationRecord(ctx context.Context, private privateStateReader) (*organizationRecord, diag.Diagnostics) {
	value, diags := private.GetKey(ctx, organizationContextKey)
	if diags.HasError() || len(value) == 0 {
		return nil, diags
	}
	var record organizationRecord
	if err := json.Unmarshal(value, &record); err != nil || record.ID == "" {
		diags.AddError("Unreadable organization record",
			fmt.Sprintf("The private state %q of this resource is %q, not an organization record. "+
				"Remove the resource from state with terraform state rm and import it again.", organizationContextKey, value))
		return nil, diags
	}
	return &record, diags
}

// findOrganization returns the instance organization that match selects, or
// nil. With relist, a miss lists the organizations again, because Terraform
// may have created the organization after the first list.
func findOrganization(
	ctx context.Context, c *logclient.APIClient, relist bool, match func(logclient.OrganizationRead) bool,
) (*logclient.OrganizationRead, error) {
	for _, fresh := range []bool{false, true} {
		if fresh && !relist {
			break
		}
		list, err := c.Organizations(ctx, fresh)
		var apiErr *logclient.APIError
		if errors.As(err, &apiErr) && (apiErr.StatusCode == http.StatusUnauthorized || apiErr.StatusCode == http.StatusForbidden) {
			return nil, fmt.Errorf("acting in another organization needs an instance-admin API key (scope instance:admin) "+
				"created in the self-hosted admin organization, and this key cannot list the instance organizations: %w", err)
		}
		if err != nil {
			return nil, fmt.Errorf("list the instance organizations: %w", err)
		}
		for i := range list {
			if match(list[i]) {
				return &list[i], nil
			}
		}
	}
	return nil, nil
}

// withExchangeFallback runs call with client and, when the credential alone
// fails, once more through the exchange. It applies to a resource whose state
// names an organization but has no record: one created with a key of its own
// organization that cannot read it. A later instance-admin key reaches the
// resource only through the exchange, and the direct request fails with 401,
// 403, or a 404 from the admin organization. The exchange result replaces the
// first one when it is a success or a 404. A key that cannot exchange keeps
// its own answer. A 404 is a deleted resource to every caller, so when the
// exchange fails without refusing the key, such as on a timeout, that failure
// is the answer and the resource stays. It returns the client that gave the
// result.
func withExchangeFallback(
	client *logclient.APIClient, organization types.String, call func(*logclient.APIClient) error,
) (*logclient.APIClient, error) {
	err := call(client)
	var apiErr *logclient.APIError
	if err == nil || client.Organization() != "" || !organizationSet(organization) || !errors.As(err, &apiErr) {
		return client, err
	}
	switch apiErr.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
	default:
		return client, err
	}
	exchange := client.ForOrganization(organization.ValueString())
	exchangeErr := call(exchange)
	switch {
	case exchangeErr == nil || logclient.IsNotFoundError(exchangeErr):
		return exchange, exchangeErr
	case apiErr.StatusCode == http.StatusNotFound && !logclient.IsExchangeRefusalError(exchangeErr):
		return client, exchangeErr
	}
	return client, err
}

const organizationDeletedDetail = "The organization of this resource was deleted outside Terraform. " +
	"Run terraform apply again: the refresh removes the resource from state."

// checkOrganizationForRead starts Read of a resource that acts in the
// provider organization. It reports whether Read may go on. It records the
// organization for a state without a record, and removes the resource from
// state when its organization was deleted.
func checkOrganizationForRead(ctx context.Context, c *logclient.APIClient, req resource.ReadRequest, resp *resource.ReadResponse) bool {
	client, found, diags := clientForState(ctx, c, types.StringNull(), req.Private)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return false
	}
	if !found {
		resp.State.RemoveResource(ctx)
		return false
	}
	resp.Diagnostics.Append(saveOrganizationContext(ctx, resp.Private, client)...)
	return !resp.Diagnostics.HasError()
}

// checkOrganizationForUpdate starts Update of a resource that acts in the
// provider organization. It reports whether Update may go on.
func checkOrganizationForUpdate(ctx context.Context, c *logclient.APIClient, req resource.UpdateRequest, resp *resource.UpdateResponse) bool {
	_, found, diags := clientForState(ctx, c, types.StringNull(), req.Private)
	resp.Diagnostics.Append(diags...)
	if !found {
		resp.Diagnostics.AddError("Organization deleted", organizationDeletedDetail)
	}
	return !resp.Diagnostics.HasError()
}

// checkOrganizationForDelete starts Delete of a resource that acts in the
// provider organization. It reports whether Delete must call the API: a
// resource whose organization was deleted is already gone.
func checkOrganizationForDelete(ctx context.Context, c *logclient.APIClient, req resource.DeleteRequest, resp *resource.DeleteResponse) bool {
	_, found, diags := clientForState(ctx, c, types.StringNull(), req.Private)
	resp.Diagnostics.Append(diags...)
	return found && !resp.Diagnostics.HasError()
}
