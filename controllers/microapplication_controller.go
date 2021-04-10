/*
Copyright 2021.

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

package controllers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	kubeyaml "k8s.io/apimachinery/pkg/util/yaml"

	//"k8s.io/client-go/pkg/apis/authorization"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	git "github.com/go-git/go-git/v5" // with go modules enabled (GO111MODULE=on or outside GOPATH)

	argoprojiov1alpha1 "github.com/sbose78/micro-application/api/v1alpha1"
	authorization "k8s.io/api/authorization/v1"
)

// UserApplicationReconciler reconciles a MicroApplication object
type UserApplicationReconciler struct {
	client.Client
	Log    logr.Logger
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=argoproj.io,resources=microapplications,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=argoproj.io,resources=microapplications/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=argoproj.io,resources=microapplications/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the MicroApplication object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.7.2/pkg/reconcile
func (r *UserApplicationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = r.Log.WithValues("microapplication", req.NamespacedName)

	// Controller
	// your logic here
	userApplication := &argoprojiov1alpha1.MicroApplication{}
	userApplication.Name = req.Name
	userApplication.Namespace = req.Namespace
	err := r.Get(ctx, req.NamespacedName, userApplication)
	if err != nil {
		return ctrl.Result{}, nil
	}

	// Ensure latest revision is checkedout
	namespacePath := fmt.Sprintf("/tmp/%s", userApplication.Namespace)
	err = os.Mkdir(namespacePath, 0755)
	if err != nil {
		println(err)
	}

	namespacedResourcePath := fmt.Sprintf("/tmp/%s/%s", userApplication.Namespace, userApplication.Name)
	err = os.Mkdir(namespacedResourcePath, 0755)
	if err != nil {
		println(err)
	}

	// Update code from Git, ignore everything on the way ;)

	_, err = git.PlainClone(namespacedResourcePath, false, &git.CloneOptions{
		URL:      userApplication.Spec.RepoURL,
		Progress: os.Stdout,
	})
	if err != nil {
		println(err)
	}

	rr, err := git.PlainOpen(namespacedResourcePath)
	if err != nil {
		println(err)
	}
	w, err := rr.Worktree()
	if err != nil {
		println(err)
	}

	err = w.Pull(&git.PullOptions{RemoteName: "origin"})
	if err != nil {
		println(err)
	}

	resources, _, err := parseManifests(namespacedResourcePath, []string{userApplication.Spec.Path})
	if err != nil {
		println(err)
	}

	creator := userApplication.Annotations["microapplication.argoproj.io/creator"]

	isAllowed := true
	for _, resource := range resources {

		targetNs := resource.GetNamespace()
		if targetNs == "" {
			targetNs = userApplication.Namespace
		}

		plural, _ := meta.UnsafeGuessKindToResource(resource.GroupVersionKind())
		sar := authorization.SubjectAccessReview{
			Spec: authorization.SubjectAccessReviewSpec{
				User: creator,

				ResourceAttributes: &authorization.ResourceAttributes{
					Group:     resource.GroupVersionKind().Group,
					Version:   resource.GroupVersionKind().Version,
					Resource:  plural.Resource, // singular.Resource,
					Namespace: targetNs,
					Name:      resource.GetName(),
					Verb:      "create",
				},
			},
		}
		fmt.Println("Checking permissions for ", sar.Spec)

		err = r.Create(ctx, &sar, &client.CreateOptions{})
		if err != nil {
			return ctrl.Result{}, nil
		}
		fmt.Println("sar.Status.Allowed", sar.Status.Allowed)
		isAllowed = sar.Status.Allowed
		if !isAllowed {
			break
		}
	}

	for _, resource := range resources {
		targetNs := resource.GetNamespace()
		if targetNs == "" {
			targetNs = userApplication.Namespace
		}

		fetchedResource := &unstructured.Unstructured{}
		fetchedResource.SetKind(resource.GetKind())
		fetchedResource.SetAPIVersion(resource.GetAPIVersion())

		err = r.Get(ctx, types.NamespacedName{Name: resource.GetName(), Namespace: targetNs}, fetchedResource)
		isFound := true
		if err != nil {
			// for now assume!
			isFound = false
		}

		// yet to figure out how to do a diff

		if !isFound {
			if resource.GetNamespace() == "" {
				resource.SetNamespace(userApplication.Namespace)
			}
			err = r.Create(ctx, resource, &client.CreateOptions{})
			if err != nil {
				return ctrl.Result{}, nil
			}
		}
	}

	userApplication.Status.Allowed = isAllowed
	err = r.Status().Update(ctx, userApplication, &client.UpdateOptions{})
	if err != nil {
		return ctrl.Result{}, nil
	}

	return ctrl.Result{}, nil
}

// Copied from https://github.com/argoproj/gitops-engine/

// SplitYAML splits a YAML file into unstructured objects. Returns list of all unstructured objects
// found in the yaml. If an error occurs, returns objects that have been parsed so far too.
func SplitYAML(yamlData []byte) ([]*unstructured.Unstructured, error) {
	// Similar way to what kubectl does
	// https://github.com/kubernetes/cli-runtime/blob/master/pkg/resource/visitor.go#L573-L600
	// Ideally k8s.io/cli-runtime/pkg/resource.Builder should be used instead of this method.
	// E.g. Builder does list unpacking and flattening and this code does not.
	d := kubeyaml.NewYAMLOrJSONDecoder(bytes.NewReader(yamlData), 4096)
	var objs []*unstructured.Unstructured
	for {
		ext := runtime.RawExtension{}
		if err := d.Decode(&ext); err != nil {
			if err == io.EOF {
				break
			}
			return objs, fmt.Errorf("failed to unmarshal manifest: %v", err)
		}
		ext.Raw = bytes.TrimSpace(ext.Raw)
		if len(ext.Raw) == 0 || bytes.Equal(ext.Raw, []byte("null")) {
			continue
		}
		u := &unstructured.Unstructured{}
		if err := json.Unmarshal(ext.Raw, u); err != nil {
			return objs, fmt.Errorf("failed to unmarshal manifest: %v", err)
		}

		objs = append(objs, u)
	}
	return objs, nil
}

// copied from https://github.com/argoproj/gitops-engine/
func parseManifests(repoPath string, paths []string) ([]*unstructured.Unstructured, string, error) {

	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = repoPath
	revision, err := cmd.CombinedOutput()
	if err != nil {
		return nil, "", err
	}
	var res []*unstructured.Unstructured
	for i := range paths {
		if err := filepath.Walk(filepath.Join(repoPath, paths[i]), func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			if ext := strings.ToLower(filepath.Ext(info.Name())); ext != ".json" && ext != ".yml" && ext != ".yaml" {
				return nil
			}

			fmt.Println(path)
			data, err := ioutil.ReadFile(path)
			if err != nil {
				return err
			}

			items, err := SplitYAML(data)
			if err != nil {
				return err
			}

			res = append(res, items...)
			return nil
		}); err != nil {
			return nil, "", err
		}
	}
	return res, string(revision), nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *UserApplicationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&argoprojiov1alpha1.MicroApplication{}).
		Complete(r)
}
