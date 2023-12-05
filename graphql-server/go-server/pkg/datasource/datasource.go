/*
Copyright 2023 KubeAGI.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package datasource

import (
	"context"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/kubeagi/arcadia/api/base/v1alpha1"
	"github.com/kubeagi/arcadia/graphql-server/go-server/graph/generated"
	"github.com/kubeagi/arcadia/pkg/utils"
)

var dsSchema = schema.GroupVersionResource{
	Group:    v1alpha1.GroupVersion.Group,
	Version:  v1alpha1.GroupVersion.Version,
	Resource: "datasources",
}

func datasource2model(obj *unstructured.Unstructured) *generated.Datasource {
	datasource := &v1alpha1.Datasource{}
	if err := utils.UnstructuredToStructured(obj, datasource); err != nil {
		return &generated.Datasource{}
	}

	id := string(datasource.GetUID())

	labels := make(map[string]interface{})
	for k, v := range obj.GetLabels() {
		labels[k] = v
	}
	annotations := make(map[string]interface{})
	for k, v := range obj.GetAnnotations() {
		annotations[k] = v
	}

	creationtimestamp := datasource.GetCreationTimestamp().Time

	// conditioned status
	condition := datasource.Status.GetCondition(v1alpha1.TypeReady)
	updateTime := condition.LastTransitionTime.Time
	status := string(condition.Status)

	// parse endpoint
	endpoint := generated.Endpoint{
		URL:      &datasource.Spec.Enpoint.URL,
		Insecure: &datasource.Spec.Enpoint.Insecure,
	}
	if datasource.Spec.Enpoint.AuthSecret != nil {
		endpoint.AuthSecret = &generated.TypedObjectReference{
			Kind:      "Secret",
			Name:      datasource.Spec.Enpoint.AuthSecret.Name,
			Namespace: datasource.Spec.Enpoint.AuthSecret.Namespace,
		}
	}

	// parse oss
	oss := generated.Oss{}
	if datasource.Spec.OSS != nil {
		oss.Bucket = &datasource.Spec.OSS.Bucket
		oss.Object = &datasource.Spec.OSS.Object
	}

	md := generated.Datasource{
		ID:                &id,
		Name:              datasource.Name,
		Namespace:         datasource.Namespace,
		Labels:            labels,
		Annotations:       annotations,
		DisplayName:       &datasource.Spec.DisplayName,
		Description:       &datasource.Spec.Description,
		Endpoint:          &endpoint,
		Oss:               &oss,
		Status:            &status,
		CreationTimestamp: &creationtimestamp,
		UpdateTimestamp:   &updateTime,
	}
	return &md
}

func CreateDatasource(ctx context.Context, c dynamic.Interface, input generated.CreateDatasourceInput) (*generated.Datasource, error) {
	var displayname, description string
	var insecure bool

	if input.Description != nil {
		description = *input.Description
	}
	if input.DisplayName != nil {
		displayname = *input.DisplayName
	}
	if input.Endpointinput.Insecure != nil {
		insecure = *input.Endpointinput.Insecure
	}

	datasource := v1alpha1.Datasource{
		ObjectMeta: metav1.ObjectMeta{
			Name:      input.Name,
			Namespace: input.Namespace,
		},
		TypeMeta: metav1.TypeMeta{
			Kind:       "Datasource",
			APIVersion: v1alpha1.GroupVersion.String(),
		},
		Spec: v1alpha1.DatasourceSpec{
			CommonSpec: v1alpha1.CommonSpec{
				DisplayName: displayname,
				Description: description,
			},
			Enpoint: v1alpha1.Endpoint{
				URL:      input.Endpointinput.URL,
				Insecure: insecure,
			},
		},
	}

	if input.Endpointinput.AuthSecret != nil {
		datasource.Spec.Enpoint.AuthSecret = &v1alpha1.TypedObjectReference{
			Kind:      "Secret",
			Name:      input.Endpointinput.AuthSecret.Name,
			Namespace: &input.Namespace,
		}
	}

	if input.Ossinput != nil {
		datasource.Spec.OSS = &v1alpha1.OSS{
			Bucket: input.Ossinput.Bucket,
		}
		if input.Ossinput.Object != nil {
			datasource.Spec.OSS.Object = *input.Ossinput.Object
		}
	}

	unstructuredDatasource, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&datasource)
	if err != nil {
		return nil, err
	}
	obj, err := c.Resource(schema.GroupVersionResource{Group: v1alpha1.GroupVersion.Group, Version: v1alpha1.GroupVersion.Version, Resource: "datasources"}).
		Namespace(input.Namespace).Create(ctx, &unstructured.Unstructured{Object: unstructuredDatasource}, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}
	ds := datasource2model(obj)
	return ds, nil
}

func UpdateDatasource(ctx context.Context, c dynamic.Interface, input *generated.UpdateDatasourceInput) (*generated.Datasource, error) {
	resource := c.Resource(schema.GroupVersionResource{Group: v1alpha1.GroupVersion.Group, Version: v1alpha1.GroupVersion.Version, Resource: "datasources"})
	obj, err := resource.Namespace(input.Namespace).Get(ctx, input.Name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	datasource := &v1alpha1.Datasource{}
	if err := utils.UnstructuredToStructured(obj, datasource); err != nil {
		return nil, err
	}

	l := make(map[string]string)
	for k, v := range input.Labels {
		l[k] = v.(string)
	}
	datasource.SetLabels(l)

	a := make(map[string]string)
	for k, v := range input.Annotations {
		a[k] = v.(string)
	}
	datasource.SetAnnotations(a)

	if input.DisplayName != nil {
		datasource.Spec.DisplayName = *input.DisplayName
	}
	if input.Description != nil {
		datasource.Spec.Description = *input.Description
	}

	// Update endpoint
	if input.Endpointinput != nil {
		endpoint := v1alpha1.Endpoint{
			URL: input.Endpointinput.URL,
		}
		if input.Endpointinput.Insecure != nil {
			endpoint.Insecure = *input.Endpointinput.Insecure
		}
		if input.Endpointinput.AuthSecret != nil {
			endpoint.AuthSecret = &v1alpha1.TypedObjectReference{
				Name: input.Endpointinput.AuthSecret.Name,
				Kind: "Secret",
			}
		}
		datasource.Spec.Enpoint = endpoint
	}

	// Update ossinput
	if input.Ossinput != nil {
		oss := &v1alpha1.OSS{
			Bucket: input.Ossinput.Bucket,
		}
		if input.Ossinput.Object != nil {
			oss.Object = *input.Ossinput.Object
		}
		datasource.Spec.OSS = oss
	}

	unstructuredDatasource, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&datasource)
	if err != nil {
		return nil, err
	}

	updatedObject, err := resource.Namespace(input.Namespace).Update(ctx, &unstructured.Unstructured{Object: unstructuredDatasource}, metav1.UpdateOptions{})
	if err != nil {
		return nil, err
	}
	ds := datasource2model(updatedObject)
	return ds, nil
}

func DeleteDatasources(ctx context.Context, c dynamic.Interface, input *generated.DeleteCommonInput) (*string, error) {
	name := ""
	labelSelector, fieldSelector := "", ""
	if input.Name != nil {
		name = *input.Name
	}
	if input.FieldSelector != nil {
		fieldSelector = *input.FieldSelector
	}
	if input.LabelSelector != nil {
		labelSelector = *input.LabelSelector
	}
	resource := c.Resource(schema.GroupVersionResource{Group: v1alpha1.GroupVersion.Group, Version: v1alpha1.GroupVersion.Version, Resource: "datasources"})
	if name != "" {
		err := resource.Namespace(input.Namespace).Delete(ctx, name, metav1.DeleteOptions{})
		if err != nil {
			return nil, err
		}
	} else {
		err := resource.Namespace(input.Namespace).DeleteCollection(ctx, metav1.DeleteOptions{}, metav1.ListOptions{
			LabelSelector: labelSelector,
			FieldSelector: fieldSelector,
		})
		if err != nil {
			return nil, err
		}
	}
	return nil, nil
}
func ListDatasources(ctx context.Context, c dynamic.Interface, input generated.ListCommonInput) (*generated.PaginatedResult, error) {
	keyword, labelSelector, fieldSelector := "", "", ""
	page, pageSize := 1, 10
	if input.Keyword != nil {
		keyword = *input.Keyword
	}
	if input.FieldSelector != nil {
		fieldSelector = *input.FieldSelector
	}
	if input.LabelSelector != nil {
		labelSelector = *input.LabelSelector
	}
	if input.Page != nil && *input.Page > 0 {
		page = *input.Page
	}
	if input.PageSize != nil && *input.PageSize > 0 {
		pageSize = *input.PageSize
	}

	listOptions := metav1.ListOptions{
		LabelSelector: labelSelector,
		FieldSelector: fieldSelector,
	}

	datasList, err := c.Resource(dsSchema).Namespace(input.Namespace).List(ctx, listOptions)
	if err != nil {
		return nil, err
	}
	sort.Slice(datasList.Items, func(i, j int) bool {
		return datasList.Items[i].GetCreationTimestamp().After(datasList.Items[j].GetCreationTimestamp().Time)
	})

	totalCount := len(datasList.Items)

	result := make([]generated.PageNode, 0, pageSize)
	for _, u := range datasList.Items {
		m := datasource2model(&u)
		// filter based on `keyword`
		if keyword != "" {
			if !strings.Contains(m.Name, keyword) && !strings.Contains(*m.DisplayName, keyword) {
				continue
			}
		}
		result = append(result, m)

		// break if page size matches
		if len(result) == pageSize {
			break
		}
	}

	end := page * pageSize
	if end > totalCount {
		end = totalCount
	}

	return &generated.PaginatedResult{
		TotalCount:  totalCount,
		HasNextPage: end < totalCount,
		Nodes:       result,
	}, nil
}

func ReadDatasource(ctx context.Context, c dynamic.Interface, name, namespace string) (*generated.Datasource, error) {
	resource := c.Resource(dsSchema)
	u, err := resource.Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	return datasource2model(u), nil
}
