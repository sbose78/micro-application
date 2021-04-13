# Micro Application Controller

## About

A controller which creates resources from Git based on the creator's permissions on the cluster.

```
apiVersion: argoproj.io/v1alpha1
kind: MicroApplication
metadata:
  namespace: default
  name: example
  annotations:
    generated-creator: consoledeveloper
spec:
  path: developer/new-app
  repoURL: 'https://github.com/sbose78/gitops-samples'
status:
  allowed: true
  lastSync: '2021-04-12 05:01:44.550065783 +0000 UTC m=+1472.467854266'
```

If the user `consoledeveloper` doesn't have the permissions to create the resources in `.spec.repoURL`, the controller will not permit creation of these resources.

Conversely, if the user `consoledeveloper` has permissions to create the resources defined in `.spec.repoURL`, the controller creates them for you.

## How this works

1. A mutating [webhook](https://github.com/sbose78/micro-application-admission) modfies the `MicroApplication` resource before admission to write/overwite the annotation named `generated-creator`.

2. The MicroApplication controller does a *`SubjectAccessReview`* to verify if the `generated-creator` is allowed to create the resources in `.spec.repoURL`+`.spec.path`

3. The controller polls the Git repository at frequent intervals to pull down the latest changes from git and applies them.

## Install

1. Install the mutating admission controller webhook.

On OpenShift,

```
$ kubectl apply -f https://github.com/sbose78/micro-application-admission/blob/main/manifests/openshift/install.yaml
```

On other distributions of Kubernetes,

```
$ kubectl apply -f https://github.com/sbose78/micro-application-admission/blob/main/manifests/kubernetes/install.yaml
```

2. Install the MicroApplication Controller

```
kubectl apply -f install.yaml
```
Or,

```
$ kubectl apply -f https://github.com/sbose78/micro-application/blob/main/install.yaml
```


