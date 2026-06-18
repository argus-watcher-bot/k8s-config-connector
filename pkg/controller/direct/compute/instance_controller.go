// Copyright 2026 Google LLC
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

package compute

import (
	"context"
	"fmt"
	"strings"

	"k8s.io/klog/v2"

	gcp "cloud.google.com/go/compute/apiv1"
	computepb "cloud.google.com/go/compute/apiv1/computepb"
	krm "github.com/GoogleCloudPlatform/k8s-config-connector/apis/compute/v1beta1"
	"github.com/GoogleCloudPlatform/k8s-config-connector/pkg/config"
	"github.com/GoogleCloudPlatform/k8s-config-connector/pkg/controller/direct"
	"github.com/GoogleCloudPlatform/k8s-config-connector/pkg/controller/direct/directbase"
	"github.com/GoogleCloudPlatform/k8s-config-connector/pkg/controller/direct/registry"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func init() {
	registry.RegisterModel(krm.ComputeInstanceGVK, NewInstanceModel)
}

func NewInstanceModel(ctx context.Context, config *config.ControllerConfig) (directbase.Model, error) {
	return &instanceModel{config: config}, nil
}

type instanceModel struct {
	config *config.ControllerConfig
}

// model implements the Model interface.
var _ directbase.Model = &instanceModel{}

type instanceAdapter struct {
	id              *krm.InstanceIdentity
	instancesClient *gcp.InstancesClient
	desired         *krm.ComputeInstance
	actual          *computepb.Instance
	reader          client.Reader
}

var _ directbase.Adapter = &instanceAdapter{}

func (m *instanceModel) AdapterForObject(ctx context.Context, op *directbase.AdapterForObjectOperation) (directbase.Adapter, error) {
	u := op.GetUnstructured()
	reader := op.Reader
	obj := &krm.ComputeInstance{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &obj); err != nil {
		return nil, fmt.Errorf("error converting to %T: %w", obj, err)
	}

	id, err := krm.NewInstanceIdentity(ctx, reader, obj, u)
	if err != nil {
		return nil, err
	}

	instanceAdapter := &instanceAdapter{
		id:      id,
		desired: obj,
		reader:  reader,
	}

	gcpClient, err := newGCPClient(m.config)
	if err != nil {
		return nil, fmt.Errorf("building gcp client: %w", err)
	}

	instancesClient, err := gcpClient.newInstancesClient(ctx)
	if err != nil {
		return nil, err
	}
	instanceAdapter.instancesClient = instancesClient

	return instanceAdapter, nil
}

func (m *instanceModel) AdapterForURL(ctx context.Context, url string) (directbase.Adapter, error) {
	// TODO: Support URLs
	return nil, nil
}

func (a *instanceAdapter) Find(ctx context.Context) (bool, error) {
	log := klog.FromContext(ctx)
	log.V(2).Info("getting ComputeInstance", "name", a.id)

	instance, err := a.get(ctx)
	if err != nil {
		if direct.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("getting ComputeInstance %q: %w", a.id, err)
	}
	a.actual = instance
	return true, nil
}

func (a *instanceAdapter) Create(ctx context.Context, createOp *directbase.CreateOperation) error {
	log := klog.FromContext(ctx)
	log.V(2).Info("creating ComputeInstance", "name", a.id)
	mapCtx := &direct.MapContext{}

	desired := a.desired.DeepCopy()
	instance := ComputeInstanceSpec_v1beta1_ToProto(mapCtx, &desired.Spec)
	if mapCtx.Err() != nil {
		return mapCtx.Err()
	}

	parent := a.id.Parent()

	tokens := strings.Split(a.id.String(), "/")
	instance.Name = direct.LazyPtr(tokens[len(tokens)-1])

	req := &computepb.InsertInstanceRequest{
		Project:          parent.ProjectID,
		Zone:             parent.Location,
		InstanceResource: instance,
	}
	op, err := a.instancesClient.Insert(ctx, req)
	if err != nil {
		return fmt.Errorf("creating ComputeInstance %s: %w", a.id, err)
	}
	if !op.Done() {
		err = op.Wait(ctx)
		if err != nil {
			return fmt.Errorf("waiting ComputeInstance %s create failed: %w", a.id, err)
		}
	}
	log.V(2).Info("successfully created ComputeInstance", "name", a.id)

	// Get the created resource
	created, err := a.get(ctx)
	if err != nil {
		return fmt.Errorf("getting ComputeInstance %s: %w", a.id, err)
	}

	status := ComputeInstanceStatus_v1beta1_FromProto(mapCtx, created)
	if mapCtx.Err() != nil {
		return mapCtx.Err()
	}

	return createOp.UpdateStatus(ctx, status, nil)
}

func (a *instanceAdapter) Update(ctx context.Context, updateOp *directbase.UpdateOperation) error {
	log := klog.FromContext(ctx)
	log.V(2).Info("updating ComputeInstance", "name", a.id)
	mapCtx := &direct.MapContext{}

	desired := a.desired.DeepCopy()
	instance := ComputeInstanceSpec_v1beta1_ToProto(mapCtx, &desired.Spec)
	if mapCtx.Err() != nil {
		return mapCtx.Err()
	}

	parent := a.id.Parent()
	tokens := strings.Split(a.id.String(), "/")
	instance.Name = direct.LazyPtr(tokens[len(tokens)-1])

	// Compute instances can be updated with Update/Patch or specific APIs.
	// For simplicity, let's implement the Update using Update method.
	req := &computepb.UpdateInstanceRequest{
		Project:          parent.ProjectID,
		Zone:             parent.Location,
		Instance:         tokens[len(tokens)-1],
		InstanceResource: instance,
	}
	op, err := a.instancesClient.Update(ctx, req)
	if err != nil {
		return fmt.Errorf("updating ComputeInstance %s: %w", a.id, err)
	}
	if !op.Done() {
		err = op.Wait(ctx)
		if err != nil {
			return fmt.Errorf("waiting ComputeInstance %s update failed: %w", a.id, err)
		}
	}
	log.V(2).Info("successfully updated ComputeInstance", "name", a.id)

	updated, err := a.get(ctx)
	if err != nil {
		return fmt.Errorf("getting ComputeInstance %s: %w", a.id, err)
	}

	status := ComputeInstanceStatus_v1beta1_FromProto(mapCtx, updated)
	if mapCtx.Err() != nil {
		return mapCtx.Err()
	}

	return updateOp.UpdateStatus(ctx, status, nil)
}

func (a *instanceAdapter) Export(ctx context.Context) (*unstructured.Unstructured, error) {
	if a.actual == nil {
		return nil, fmt.Errorf("instance %s not found", a.id)
	}

	mc := &direct.MapContext{}
	spec := ComputeInstanceSpec_v1beta1_FromProto(mc, a.actual)
	specObj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(spec)
	if err != nil {
		return nil, fmt.Errorf("error converting instance spec to unstructured: %w", err)
	}

	u := &unstructured.Unstructured{
		Object: make(map[string]interface{}),
	}
	u.SetGroupVersionKind(krm.ComputeInstanceGVK)

	if err := unstructured.SetNestedField(u.Object, specObj, "spec"); err != nil {
		return nil, fmt.Errorf("setting spec: %w", err)
	}

	return u, nil
}

// Delete implements the Adapter interface.
func (a *instanceAdapter) Delete(ctx context.Context, deleteOp *directbase.DeleteOperation) (bool, error) {
	log := klog.FromContext(ctx)
	log.V(2).Info("deleting ComputeInstance", "name", a.id)

	parent := a.id.Parent()
	tokens := strings.Split(a.id.String(), "/")

	req := &computepb.DeleteInstanceRequest{
		Project:  parent.ProjectID,
		Zone:     parent.Location,
		Instance: tokens[len(tokens)-1],
	}
	op, err := a.instancesClient.Delete(ctx, req)
	if err != nil {
		return false, fmt.Errorf("deleting ComputeInstance %s: %w", a.id, err)
	}
	if !op.Done() {
		err = op.Wait(ctx)
		if err != nil {
			return false, fmt.Errorf("waiting ComputeInstance %s delete failed: %w", a.id, err)
		}
	}
	log.V(2).Info("successfully deleted ComputeInstance", "name", a.id)
	return true, nil
}

func (a *instanceAdapter) get(ctx context.Context) (*computepb.Instance, error) {
	parent := a.id.Parent()
	tokens := strings.Split(a.id.String(), "/")

	req := &computepb.GetInstanceRequest{
		Project:  parent.ProjectID,
		Zone:     parent.Location,
		Instance: tokens[len(tokens)-1],
	}
	return a.instancesClient.Get(ctx, req)
}
