// Copyright 2025 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package v1beta1

import (
	"context"
	"fmt"
	"strings"

	"github.com/GoogleCloudPlatform/k8s-config-connector/apis/common"
	refsv1beta1 "github.com/GoogleCloudPlatform/k8s-config-connector/apis/refs/v1beta1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// InstanceIdentity is the identity of a ComputeInstance.
type InstanceIdentity struct {
	parent *InstanceParent
	id     string
}

func (i *InstanceIdentity) String() string {
	return i.parent.String() + "/instances/" + i.id
}

func (i *InstanceIdentity) ID() string {
	return i.id
}

func (i *InstanceIdentity) Parent() *InstanceParent {
	return i.parent
}

type InstanceParent struct {
	ProjectID string
	Location  string
}

func (p *InstanceParent) String() string {
	return "projects/" + p.ProjectID + "/zones/" + p.Location
}

// NewInstanceIdentity builds a InstanceIdentity from the Config Connector Instance object.
func NewInstanceIdentity(ctx context.Context, reader client.Reader, obj *ComputeInstance, u *unstructured.Unstructured) (*InstanceIdentity, error) {
	// Get projectID
	projectID, err := refsv1beta1.ResolveProjectID(ctx, reader, u)
	if err != nil {
		return nil, err
	}

	// Get Location
	if obj.Spec.Zone == nil {
		return nil, fmt.Errorf("spec.zone is required")
	}
	location := *obj.Spec.Zone

	// Get desired ID
	resourceID := common.ValueOf(obj.Spec.ResourceID)
	if resourceID == "" {
		resourceID = obj.GetName()
	}
	if resourceID == "" {
		return nil, fmt.Errorf("cannot resolve resource ID")
	}

	// Use approved External
	selfLink := common.ValueOf(obj.Status.SelfLink)
	if selfLink != "" {
		// Validate desired with actual
		actualIdentity, err := ParseSelfLink(selfLink)
		if err != nil {
			return nil, err
		}
		if actualIdentity.parent.ProjectID != projectID {
			return nil, fmt.Errorf("spec.projectRef changed, expect %s, got %s", actualIdentity.parent.ProjectID, projectID)
		}
		if actualIdentity.parent.Location != location {
			return nil, fmt.Errorf("spec.location changed, expect %s, got %s", actualIdentity.parent.Location, location)
		}
		if actualIdentity.id != resourceID {
			return nil, fmt.Errorf("cannot reset `metadata.name` or `spec.resourceID` to %s, since it has already assigned to %s",
				resourceID, actualIdentity.id)
		}
	}
	return &InstanceIdentity{
		parent: &InstanceParent{
			ProjectID: projectID,
			Location:  location,
		},
		id: resourceID,
	}, nil
}

func ParseInstanceExternal(external string) (parent *InstanceParent, resourceID string, err error) {
	// e.g. projects/my-project/zones/us-central1-a/instances/my-instance
	tokens := strings.Split(external, "/")
	if len(tokens) != 6 || tokens[0] != "projects" || tokens[2] != "zones" || tokens[4] != "instances" {
		return nil, "", fmt.Errorf("format of ComputeInstance external=%q was not known (use projects/{{projectID}}/zones/{{zone}}/instances/{{instanceID}})", external)
	}
	parent = &InstanceParent{
		ProjectID: tokens[1],
		Location:  tokens[3],
	}
	resourceID = tokens[5]
	return parent, resourceID, nil
}

func ParseSelfLink(selfLink string) (InstanceIdentity, error) {
	// e.g. selfLink: https://www.googleapis.com/compute/v1/projects/my-project/zones/us-central1-a/instances/my-instance

	// Remove the service prefix if present
	path := selfLink
	if strings.HasPrefix(selfLink, "https://") {
		parts := strings.SplitN(selfLink, "/projects/", 2)
		if len(parts) != 2 {
			return InstanceIdentity{}, fmt.Errorf("invalid selfLink format: %s", selfLink)
		}
		path = "projects/" + parts[1]
	}

	parent, resourceID, err := ParseInstanceExternal(path)
	if err != nil {
		return InstanceIdentity{}, fmt.Errorf("failed to parse selfLink: %w", err)
	}
	return InstanceIdentity{
		parent: parent,
		id:     resourceID,
	}, nil
}
