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
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	kubeyaml "k8s.io/apimachinery/pkg/util/yaml"

	//"k8s.io/client-go/pkg/apis/authorization"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	git "github.com/go-git/go-git/v5" // with go modules enabled (GO111MODULE=on or outside GOPATH)

	"github.com/sbose78/micro-application/api/v1alpha1"
	argoprojiov1alpha1 "github.com/sbose78/micro-application/api/v1alpha1"
	authorization "k8s.io/api/authorization/v1"
)

// MicroApplicationReconciler reconciles a MicroApplication object
type MicroApplicationReconciler struct {
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
func (r *MicroApplicationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = r.Log.WithValues("microapplication", req.NamespacedName)

	// Controller
	// your logic here
	microApplication := &argoprojiov1alpha1.MicroApplication{}
	microApplication.Name = req.Name
	microApplication.Namespace = req.Namespace
	err := r.Get(ctx, req.NamespacedName, microApplication)
	if err != nil {
		return ctrl.Result{}, nil
	}

	// Ensure latest revision is checkedout
	namespacePath := fmt.Sprintf("/tmp/%s", microApplication.Namespace)
	os.Mkdir(namespacePath, 0755)

	namespacedResourcePath := fmt.Sprintf("/tmp/%s/%s", microApplication.Namespace, microApplication.Name)
	os.Mkdir(namespacedResourcePath, 0755)

	// Update code from Git, ignore everything on the way ;)
	cloneRepository(microApplication.Spec.RepoURL, namespacedResourcePath)

	resources, _, err := parseManifests(namespacedResourcePath, []string{microApplication.Spec.Path})
	if err != nil {
		println(err)
	}

	creator := microApplication.Annotations["generated-creator"]
	isAllowed := true

	for _, resource := range resources {

		// skip validation if the annotation isn't set.
		// this would happen if the admission controller wasn't installed.
		// Definitely not recommended but I wouldn't inconevnience you ;)
		if creator == "" {
			break
		}
		if creator == "kube:admin" {

			// kube:admin isn't a real user on the cluster
			// Given that it's a well-known user, we'll skip the SubjectAccessReview
			// check.
			isAllowed = true
			break
		}

		targetNs := resource.GetNamespace()
		if targetNs == "" {
			targetNs = microApplication.Namespace
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
			microApplication.Status.Allowed = isAllowed
			microApplication.Status.LastSync = time.Now().String()

			err = r.Status().Update(ctx, microApplication, &client.UpdateOptions{})
			if err != nil {
				fmt.Println(err)
			}
			return ctrl.Result{}, nil
		}
	}

	// kinda print, can be done better
	k := fmt.Sprintf("kubectl apply -n %s -f %s/%s ", microApplication.Namespace, namespacedResourcePath, microApplication.Spec.Path)
	fmt.Println(k)

	// actual command
	cmd := exec.Command("kubectl", "apply", "-n", microApplication.Namespace, "-f", fmt.Sprintf("%s/%s", namespacedResourcePath, microApplication.Spec.Path))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err = cmd.Run()
	if err != nil {
		fmt.Println("Error while executing command", err)
	}

	microApplication.Status.Allowed = isAllowed
	microApplication.Status.LastSync = time.Now().String()

	err = r.Status().Update(ctx, microApplication, &client.UpdateOptions{})
	if err != nil {
		fmt.Println(err)
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
func (r *MicroApplicationReconciler) SetupWithManager(mgr ctrl.Manager) error {

	// Not the ideal place, but this is where we can set things up
	if os.Getenv("INSTALL_ADMISSION_CONTROLLER") == "true" {
		installAdmissionController()
	}

	p := predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldObject := e.ObjectOld.(*v1alpha1.MicroApplication)
			newObject := e.ObjectNew.(*v1alpha1.MicroApplication)
			return oldObject.ResourceVersion == newObject.ResourceVersion
		},
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&argoprojiov1alpha1.MicroApplication{}).
		WithEventFilter(p).
		Complete(r)
}

func installAdmissionController() error {
	// TODO: Create a separate mount path.
	clonePath := "/tmp/setup/install"
	manifestPath := "manifests/openshift"

	manifestPathEnvValue := os.Getenv("ADMISSION_CONTROLLER_REPO_PATH")
	if manifestPathEnvValue != "" {
		manifestPath = manifestPathEnvValue
	}

	cloneURL := "https://github.com/sbose78/micro-application-admission"
	cloneRepository(cloneURL, clonePath)

	// print as an FYI, can be done better
	k := fmt.Sprintf("kubectl apply -f %s", clonePath)
	fmt.Println(k)

	// actual command
	cmd := exec.Command("kubectl", "apply", "-f", fmt.Sprintf("%s/%s", clonePath, manifestPath))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err := cmd.Run()
	if err != nil {
		fmt.Println("Error ", err)
	}
	return err
}

func cloneRepository(cloneURL string, clonePath string) error {

	git.PlainClone(clonePath, false, &git.CloneOptions{
		URL:      cloneURL,
		Progress: os.Stdout,
	})

	rr, err := git.PlainOpen(clonePath)
	if err != nil {
		return err
	}
	w, err := rr.Worktree()
	if err != nil {
		return err
	}

	err = w.Pull(&git.PullOptions{RemoteName: "origin"})
	if err != nil {
		return err
	}
	return err
}
