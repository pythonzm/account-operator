/*
Copyright 2024.

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

package controller

import (
	"context"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	k8srbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	rbacv1 "poorops.com/account/api/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// AccountReconciler reconciles an Account object
type AccountReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Log    logr.Logger
}

//+kubebuilder:rbac:groups=rbac.poorops.com,resources=accounts,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=rbac.poorops.com,resources=accounts/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=rbac.poorops.com,resources=accounts/finalizers,verbs=update

//+kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=serviceaccounts;secrets,verbs=*
//+kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles;rolebindings,verbs=*

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Account object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.16.0/pkg/reconcile

func (r *AccountReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	//_ = log.FromContext(ctx, "account", req.NamespacedName)
	log.Log.Info("info info")
	account := &rbacv1.Account{}
	if err := r.Get(ctx, req.NamespacedName, account); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if err := r.HandleServiceAccount(ctx, req.NamespacedName.Name, account); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.HandleSecret(ctx, req.NamespacedName.Name, account); err != nil {
		return ctrl.Result{}, err
	}

	clusterRole, err := r.HandleClusterRole(ctx, account)
	if err != nil {
		return ctrl.Result{}, nil
	}

	for _, namespace := range account.Spec.Namespaces {
		if err := r.HandleRoleBinding(ctx, account, namespace, req.NamespacedName.Name, clusterRole.Name); err != nil {
			return ctrl.Result{}, nil
		}
	}

	if err := r.DeleteExcludeRoleBinding(ctx, account.Spec.Namespaces, req.NamespacedName.Name); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *AccountReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&rbacv1.Account{}).
		Complete(r)
}

func (r *AccountReconciler) HandleServiceAccount(ctx context.Context, saName string, owner client.Object) error {
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      saName,
			Namespace: metav1.NamespaceDefault,
		},
	}
	_, err := r.MakeReference(ctx, owner, sa)
	return err
}

func (r *AccountReconciler) HandleSecret(ctx context.Context, saName string, account *rbacv1.Account) error {
	secretName := saName + "-secret"
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:        secretName,
			Namespace:   metav1.NamespaceDefault,
			Annotations: map[string]string{"kubernetes.io/service-account.name": saName},
		},
		Type: corev1.SecretTypeServiceAccountToken,
	}
	result, err := r.MakeReference(ctx, account, secret)
	if err != nil {
		return err
	}

	if result != controllerutil.OperationResultNone {
		for {
			_ = r.Get(ctx, types.NamespacedName{
				Namespace: metav1.NamespaceDefault,
				Name:      secretName,
			}, secret)

			_, exists := secret.Data["token"]
			if exists {
				break
			}
		}

		account.Status.Token = string(secret.Data["token"])

		if err := r.Status().Update(ctx, account); err != nil {
			r.Log.Error(err, "unable to update Account status")
			return err
		}
	}

	return nil
}

func (r *AccountReconciler) HandleRoleBinding(ctx context.Context, owner client.Object, bindingNamespace, saName, roleRefName string) error {
	binding := &k8srbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      saName + "-role-binding",
			Namespace: bindingNamespace,
		},
		Subjects: []k8srbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      saName,
				Namespace: metav1.NamespaceDefault,
			},
		},
		RoleRef: k8srbacv1.RoleRef{
			Kind:     "ClusterRole",
			Name:     roleRefName,
			APIGroup: "rbac.authorization.k8s.io",
		},
	}

	_, err := r.MakeReference(ctx, owner, binding)
	return err
}

func (r *AccountReconciler) MakeReference(ctx context.Context, owner, controlled client.Object) (controllerutil.OperationResult, error) {
	r.Log.Info("start to create obj: " + controlled.GetName())
	if err := ctrl.SetControllerReference(owner, controlled, r.Scheme); err != nil {
		r.Log.Error(err, "unable to set ownerReference, controlled: "+controlled.GetName())
		return "", err
	}

	result, err := ctrl.CreateOrUpdate(ctx, r.Client, controlled, func() error { return nil })
	if err != nil {
		r.Log.Error(err, "unable to create or update obj: "+controlled.GetName())
		return "", err
	}
	r.Log.Info(controlled.GetName() + " " + string(result))

	return result, nil
}

func (r *AccountReconciler) HandleClusterRole(ctx context.Context, account *rbacv1.Account) (*k8srbacv1.ClusterRole, error) {
	var rules []k8srbacv1.PolicyRule
	clusterRole := &k8srbacv1.ClusterRole{}

	if len(account.Spec.Rules) > 0 {
		rules = account.Spec.Rules
		clusterRoleName := account.Name + "-cluster-role"

		clusterRole = &k8srbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterRoleName,
			},
			Rules: rules,
		}

		if _, err := r.MakeReference(ctx, account, clusterRole); err != nil {
			return nil, err
		}
	} else {
		if err := r.Get(ctx, types.NamespacedName{Name: account.Spec.ClusterRole}, clusterRole); err != nil {
			r.Log.Error(err, "unable to fetch ClusterRole: "+account.Spec.ClusterRole)
			return nil, err
		}
	}

	return clusterRole, nil
}

func (r *AccountReconciler) DeleteExcludeRoleBinding(ctx context.Context, specNamespaces []string, saName string) error {
	namespaceList := &corev1.NamespaceList{}
	err := r.List(ctx, namespaceList)
	if err != nil {
		return err
	}
	m := make(map[string]bool)
	for _, v := range specNamespaces {
		m[v] = true
	}

	for _, item := range namespaceList.Items {
		if !m[item.Name] {
			binding := &k8srbacv1.RoleBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name:      saName + "-role-binding",
					Namespace: item.Name,
				},
			}
			if err := r.Delete(ctx, binding); err != nil {
				if !errors.IsNotFound(err) {
					r.Log.Error(err, "delete role binding err, role binding: "+binding.Name)
					return err
				}
			}
		}
	}
	return nil
}
