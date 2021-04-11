# Micro Application Controller

## About

A controller which creates resources from Git based on the creator's permissions on the cluster.

```
apiVersion: argoproj.io/v1alpha1
kind: MicroApplication
metadata:
  name: microapplication-sample
  namespace: sbose
  annotations:
    "generated-creator": "consoledeveloper" 
spec:
  repoURL: https://github.com/sbose78/gitops-samples
  path: developer/new-app
```

If the user `consoledeveloper` doesn't have the permissions to create the resources in `.spec.repoURL`, the controller will not permit creation of these resources.

Conversely, if the user `consoledeveloper` has permissions to create the resources defined in `.spec.repoURL`, the controller creates them for you.

## How this works

1. A mutating [webhook](https://github.com/sbose78/micro-application-admission) modfies the `MicroApplication` resource before admission to write/overwite the annotation named `generated-creator`.

2. The controller does a *`SubjectAccessReview`* to verify if the `generated-creator` is allowed to create the resources in `.spec.repoURL`+`.spec.path`

3. The controller polls the Git repository every 10s to pull down the latest changes.




