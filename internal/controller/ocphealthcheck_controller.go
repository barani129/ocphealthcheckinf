/*
Copyright 2025 baranitharan.chittharanjan@spark.co.nz.

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
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	ocphealthcheckv1 "github.com/barani129/ocphealthcheckinf/api/v1"
	ocphealthcheckutil "github.com/barani129/ocphealthcheckinf/internal/util"
	nmstate "github.com/nmstate/kubernetes-nmstate/api/v1"
	cov1 "github.com/openshift/api/config/v1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	operatorframework "github.com/operator-framework/api/pkg/operators/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	ocmpolicy "open-cluster-management.io/governance-policy-propagator/api/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// OcpHealthCheckReconciler reconciles a OcpHealthCheck object
type OcpHealthCheckReconciler struct {
	client.Client
	RESTClient               rest.Interface
	RESTConfig               *rest.Config
	Kind                     string
	ClusterResourceNamespace string
	recorder                 record.EventRecorder
	Scheme                   *runtime.Scheme
	InformerCount            int64
}

const (
	MACHINECONFIGUPDATEDONE           = "Done"
	MACHINECONFIGUPDATEDEGRADED       = "Degraded"
	MACHINECONFIGUPDATEINPROGRESS     = "Working"
	MACHINECONFIGUPDATEUNRECONCILABLE = "Unreconcilable"
	MACHINECONFIGUPDATEREBOOTING      = "Rebooting"
)

func (r *OcpHealthCheckReconciler) newOcpHealthChecker() (client.Object, error) {
	OcpHealthcheckKind := ocphealthcheckv1.GroupVersion.WithKind(r.Kind)
	ro, err := r.Scheme.New(OcpHealthcheckKind)
	if err != nil {
		return nil, err
	}
	return ro.(client.Object), nil
}

// +kubebuilder:rbac:groups=monitoring.spark.co.nz,resources=ocphealthchecks,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=monitoring.spark.co.nz,resources=ocphealthchecks/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=monitoring.spark.co.nz,resources=ocphealthchecks/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch
// +kubebuilder:rbac:groups="machineconfiguration.openshift.io",resources=machineconfigpools,verbs=get;list;watch
// +kubebuilder:rbac:groups="config.openshift.io",resources=clusteroperators,verbs=get;list;watch
// +kubebuilder:rbac:groups="operators.coreos.com",resources=catalogsources,verbs=get;list;watch
// +kubebuilder:rbac:groups="operators.coreos.com",resources=clusterserviceversions,verbs=get;list;watch
// +kubebuilder:rbac:groups="nmstate.io",resources=nodenetworkconfigurationpolicies,verbs=get;list;watch
// +kubebuilder:rbac:groups="policy.open-cluster-management.io",resources=policies,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the OcpHealthCheck object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.20.0/pkg/reconcile
func (r *OcpHealthCheckReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, err error) {
	_ = log.FromContext(ctx)

	ocpScan, err := r.newOcpHealthChecker()
	if err != nil {
		log.Log.Error(err, "unrecognized ocphealthcheck type")
		return ctrl.Result{}, err
	}
	if err = r.Get(ctx, req.NamespacedName, ocpScan); err != nil {
		if err := client.IgnoreNotFound(err); err != nil {
			return ctrl.Result{}, fmt.Errorf("unable to retrieve OcpHealthCheck")
		}
		log.Log.Info("OcpHealthCheck is not found")
		return ctrl.Result{}, nil
	}
	spec, status, err := ocphealthcheckutil.GetSpecAndStatus(ocpScan)
	if err != nil {
		log.Log.Error(err, "unable to retrieve OcpHealthCheck spec and status")
		return ctrl.Result{}, err
	}

	// switch ocpScan.(type) {
	// case *ocphealthcheckv1.OcpHealthCheck:
	// 	// do nothing
	// default:
	// 	log.Log.Error(fmt.Errorf("unexpected ocphealthcheckscan object type: %s", ocpScan), "not retrying")
	// 	return ctrl.Result{}, nil
	// }

	// report gives feedback by updating the Ready condition of the ocphealthcheck scan
	report := func(conditionStatus ocphealthcheckv1.ConditionStatus, message string, err error) {
		eventType := corev1.EventTypeNormal
		if err != nil {
			log.Log.Error(err, message)
			eventType = corev1.EventTypeWarning
			message = fmt.Sprintf("%s: %v", message, err)
		} else {
			log.Log.Info(message)
		}
		r.recorder.Event(ocpScan, eventType, ocphealthcheckv1.EventReasonIssuerReconciler, message)
		ocphealthcheckutil.SetReadyCondition(status, conditionStatus, ocphealthcheckv1.EventReasonIssuerReconciler, message)
	}

	defer func() {
		if err != nil {
			report(ocphealthcheckv1.ConditionFalse, "Trouble running OcpHealthCheckScan", err)
		}
		if updateErr := r.Status().Update(ctx, ocpScan); updateErr != nil {
			err = utilerrors.NewAggregate([]error{err, updateErr})
			result = ctrl.Result{}
		}
	}()

	if readyCond := ocphealthcheckutil.GetReadyCondition(status); readyCond == nil {
		report(ocphealthcheckv1.ConditionUnknown, "First Seen", nil)
		return ctrl.Result{}, nil
	}

	config, err := rest.InClusterConfig()
	if err != nil {
		return ctrl.Result{}, err
	}

	clientset, err := dynamic.NewForConfig(config)
	if err != nil {
		return ctrl.Result{}, err
	}

	staticClientSet, err := kubernetes.NewForConfig(config)
	if err != nil {
		return ctrl.Result{}, err
	}

	var runningHost string
	domain, err := ocphealthcheckutil.GetAPIName(*staticClientSet)
	if err == nil && domain == "" {
		if spec.Cluster != nil {
			runningHost = *spec.Cluster
		}
	} else if err == nil && domain != "" {
		runningHost = domain
	} else {
		log.Log.Error(err, "unable to retrieve ocp config")
		runningHost = "local-cluster"
	}

	podResource := schema.GroupVersionResource{
		Group:    "",
		Version:  "v1",
		Resource: "pods",
	}
	nodeResource := schema.GroupVersionResource{
		Group:    "",
		Version:  "v1",
		Resource: "nodes",
	}

	mcpResource := schema.GroupVersionResource{
		Group:    "machineconfiguration.openshift.io",
		Version:  "v1",
		Resource: "machineconfigpools",
	}

	policyResource := schema.GroupVersionResource{
		Group:    "policy.open-cluster-management.io",
		Version:  "v1",
		Resource: "policies",
	}

	coResource := schema.GroupVersionResource{
		Group:    "config.openshift.io",
		Version:  "v1",
		Resource: "clusteroperators",
	}

	nncpResource := schema.GroupVersionResource{
		Group:    "nmstate.io",
		Version:  "v1",
		Resource: "nodenetworkconfigurationpolicies",
	}

	catalogResource := schema.GroupVersionResource{
		Group:    "operators.coreos.com",
		Version:  "v1alpha1",
		Resource: "catalogsources",
	}

	csvResource := schema.GroupVersionResource{
		Group:    "operators.coreos.com",
		Version:  "v1alpha1",
		Resource: "clusterserviceversions",
	}

	b := false
	mcpParam := ocphealthcheckutil.MCPStruct{
		IsMCPInProgress: &b,
		IsNodeAffected:  &b,
		MCPAnnoNode:     "",
		MCPNode:         "",
		MCPAnnoState:    "",
		MCPNodeState:    "",
	}
	nsFactory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(clientset, time.Second*15, corev1.NamespaceAll, nil)
	mcpInformer := nsFactory.ForResource(mcpResource).Informer()
	podInformer := nsFactory.ForResource(podResource).Informer()
	nodeInformer := nsFactory.ForResource(nodeResource).Informer()
	policyInformer := nsFactory.ForResource(policyResource).Informer()
	coInformer := nsFactory.ForResource(coResource).Informer()
	nncpInformer := nsFactory.ForResource(nncpResource).Informer()
	catalogInformer := nsFactory.ForResource(catalogResource).Informer()
	csvInformer := nsFactory.ForResource(csvResource).Informer()

	mux := &sync.RWMutex{}
	synced := false
	// logic for mcp handling: check if mcp is in progress, if in progress, fetch the node based on labels
	// mcp.spec.nodeSelector.matchLabels
	// check if annotation["machineconfiguration.openshift.io/state"] is set to other than Done
	// if not, assuming that mcp is actually in progress and exiting, otherwise continue with the flow
	mcpInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(oldObj, newObj interface{}) {
			mux.RLock()
			defer mux.RUnlock()
			if !synced {
				return
			}
			onMCPUpdate(newObj, staticClientSet, &mcpParam, status, spec, runningHost)
			CleanUpRunningPods(staticClientSet, spec, status, runningHost)
		},
	})
	log.Log.Info("Adding add pod events to pod informer")
	podInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(oldObj interface{}) {
			mux.RLock()
			defer mux.RUnlock()
			if !synced {
				return
			}
			ocphealthcheckutil.OnPodAdd(oldObj)
		},
		UpdateFunc: func(oldObj interface{}, newObj interface{}) {
			mux.RLock()
			defer mux.RUnlock()
			if !synced {
				return
			}
			if mcpParam.IsMCPInProgress != nil && !*mcpParam.IsMCPInProgress {
				OnPodUpdate(newObj, spec, status, runningHost, staticClientSet)
			}
		},
		DeleteFunc: func(obj interface{}) {
			mux.RLock()
			defer mux.RUnlock()
			if !synced {
				return
			}
			OnPodDelete(obj, spec, status, runningHost)
		},
	})
	nodeInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(oldObj, newObj interface{}) {
			mux.RLock()
			defer mux.RUnlock()
			if !synced {
				return
			}
			if mcpParam.IsMCPInProgress != nil && !*mcpParam.IsMCPInProgress {
				OnNodeUpdate(newObj, spec, status, runningHost, &mcpParam)
			}
		},
	})
	policyInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			mux.RLock()
			defer mux.RUnlock()
			if !synced {
				return
			}
			OnPolicyAdd(obj, spec, status)
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			mux.RLock()
			defer mux.RUnlock()
			if !synced {
				return
			}
			if mcpParam.IsMCPInProgress != nil && !*mcpParam.IsMCPInProgress {
				OnPolicyUpdate(newObj, spec, status, runningHost)
			}
		},
		DeleteFunc: func(obj interface{}) {
			mux.RLock()
			defer mux.RUnlock()
			if !synced {
				return
			}
			OnPolicyDelete(obj, spec, status, runningHost)
		},
	})
	coInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(oldObj, newObj interface{}) {
			OnCoUpdate(newObj, staticClientSet, spec, runningHost)
		},
	})
	catalogInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(oldObj, newObj interface{}) {
			OnCatalogSourceUpdate(newObj, staticClientSet, spec, runningHost)
		},
	})
	nncpInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(oldObj, newObj interface{}) {
			OnNNCPUpdate(newObj, staticClientSet, spec, runningHost)
		},
	})
	csvInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(oldObj, newObj interface{}) {
			OnCsvUpdate(newObj, staticClientSet, spec, runningHost)
		},
	})
	// go podInformer.Run(context.Background().Done())
	if r.InformerCount < 2 {
		log.Log.Info("Starting dynamic informer factory")
		nsFactory.Start(context.Background().Done())
		r.InformerCount++
	}
	// TO DO:
	// NNCP, CO, Sub, catalogsource
	log.Log.Info("Waiting for cache sync")
	isSynced := cache.WaitForCacheSync(context.Background().Done(), podInformer.HasSynced, nodeInformer.HasSynced, mcpInformer.HasSynced, policyInformer.HasSynced, coInformer.HasSynced, nncpInformer.HasSynced, catalogInformer.HasSynced, csvInformer.HasSynced)
	mux.Lock()
	synced = isSynced
	mux.Unlock()
	log.Log.Info("cache sync is completed")
	if !isSynced {
		return ctrl.Result{}, fmt.Errorf("failed to sync")
	}
	report(ocphealthcheckv1.ConditionTrue, "pod informers compiled successfully", nil)
	go func() {
		for {
			if files, err := os.ReadDir("/home/golanguser/files/"); err != nil {
				if len(files) < 1 {
					status.Healthy = true
				} else {
					status.Healthy = false
				}
			}
		}
	}()
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *OcpHealthCheckReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.recorder = mgr.GetEventRecorderFor(ocphealthcheckv1.EventSource)
	return ctrl.NewControllerManagedBy(mgr).
		For(&ocphealthcheckv1.OcpHealthCheck{}).
		Named("ocphealthcheck").
		Complete(r)
}

func onMCPUpdate(newObj interface{}, staticClientSet *kubernetes.Clientset, mcpParam *ocphealthcheckutil.MCPStruct, status *ocphealthcheckv1.OcpHealthCheckStatus, spec *ocphealthcheckv1.OcpHealthCheckSpec, runningHost string) {
	mcp := new(mcfgv1.MachineConfigPool)
	err := ocphealthcheckutil.ConvertUnStructureToStructured(newObj, mcp)
	if err != nil {
		log.Log.Error(err, "failed to convert")
		return
	}
	if spec.HubCluster != nil && *spec.HubCluster {
		if mcp.Spec.Paused {
			if mcpInProgress, node, err := ocphealthcheckutil.CheckNodeMcpAnnotations(staticClientSet, mcp.Spec.NodeSelector.MatchLabels); err != nil {
				// exiting
				return
			} else if err == nil && mcpInProgress {
				SendEmail(fmt.Sprintf("/home/golanguser/files/.%s-%s.txt", "mcp-pause", mcp.Name), "faulty", fmt.Sprintf("MachineConfig pool %s is paused and actual update is in progress and node %s's annotation has been set to other than done in cluster %s, please execute <oc get mcp %s > to validate it", mcp.Name, node, runningHost, mcp.Name), runningHost, spec)
				return
			} else {
				SendEmail(fmt.Sprintf("/home/golanguser/files/.%s-%s.txt", "mcp-pause", mcp.Name), "faulty", fmt.Sprintf("MachineConfig pool %s is paused but no actual MCP update in progress in cluster %s, please execute <oc get mcp %s > to validate it", mcp.Name, runningHost, mcp.Name), runningHost, spec)
			}
		} else {
			SendEmail(fmt.Sprintf("/home/golanguser/files/.%s-%s.txt", "mcp-pause", mcp.Name), "recovered", fmt.Sprintf("MachineConfig pool %s is unpaused in cluster %s, please execute <oc get mcp %s > to validate it", mcp.Name, runningHost, mcp.Name), runningHost, spec)
		}
	}

	for _, cond := range mcp.Status.Conditions {
		if cond.Type == "Updating" {
			if cond.Status == "True" {
				// Check node annotations to validate it
				if mcp.Spec.MachineConfigSelector.MatchLabels != nil {
					if isMcPInProgress, node, err := ocphealthcheckutil.CheckNodeMcpAnnotations(staticClientSet, mcp.Spec.NodeSelector.MatchLabels); err != nil {
						return
					} else if err == nil && isMcPInProgress {
						SendEmail(fmt.Sprintf("/home/golanguser/files/.%s-%s.txt", "mcp", mcp.Name), "faulty", fmt.Sprintf("MachineConfig pool %s update is in progress and node %s's annotation has been set to other than done in cluster %s, please execute <oc get mcp %s > to validate it", mcp.Name, node, runningHost, mcp.Name), runningHost, spec)
						return
					} else {
						if isNodeAffected, anode, err := ocphealthcheckutil.CheckNodeReadiness(staticClientSet, mcp.Spec.MachineConfigSelector.MatchLabels); err != nil {
							// unable to verify node status
							return
						} else if err == nil && isNodeAffected {
							SendEmail(fmt.Sprintf("/home/golanguser/files/.%s-%s.txt", "mcp", mcp.Name), "faulty", fmt.Sprintf("MachineConfig pool %s update has been set to true, due to possible manual action on node %s in cluster %s, please execute <oc get mcp %s > to validate it", mcp.Name, anode, runningHost, mcp.Name), runningHost, spec)
						} else {
							SendEmail(fmt.Sprintf("/home/golanguser/files/.%s-%s.txt", "mcp", mcp.Name), "faulty", fmt.Sprintf("MachineConfig pool %s update has been set to true, nodes are healthy, mcp update is probably just starting in cluster %s, please execute <oc get mcp %s > to validate it", mcp.Name, runningHost, mcp.Name), runningHost, spec)
						}
					}
				}
			} else if cond.Status == "False" {
				SendEmail(fmt.Sprintf("/home/golanguser/files/.%s-%s.txt", "mcp", mcp.Name), "recovered", fmt.Sprintf("MachineConfig pool %s update which was previously set to true is now changed to false in cluster %s, please execute <oc get mcp %s > to validate it", mcp.Name, runningHost, mcp.Name), runningHost, spec)
			}
		} else if cond.Type == "Degraded" {
			if cond.Status == "True" {
				SendEmail(fmt.Sprintf("/home/golanguser/files/.%s-%s.txt", "mcp", mcp.Name), "faulty", fmt.Sprintf("MachineConfig pool %s is degraded in cluster %s, please execute <oc get mcp %s and oc get nodes> to validate it", mcp.Name, runningHost, mcp.Name), runningHost, spec)
			} else if cond.Status == "False" {
				SendEmail(fmt.Sprintf("/home/golanguser/files/.%s-%s.txt", "mcp", mcp.Name), "recovered", fmt.Sprintf("MachineConfig pool %s is no longer degraded in cluster %s", mcp.Name, runningHost), runningHost, spec)
			}
		}
	}
}

func podLastRestartTimerUp(timeStr string) bool {
	oldTime, err := time.Parse(time.RFC3339, timeStr)
	if err != nil {
		return false
	}
	var timePast bool
	currTime := time.Now().Add(-30 * time.Minute)
	timePast = oldTime.Before(currTime)
	return timePast
}

func OnPodUpdate(newObj interface{}, spec *ocphealthcheckv1.OcpHealthCheckSpec, status *ocphealthcheckv1.OcpHealthCheckStatus, runningHost string, clientset *kubernetes.Clientset) {
	newPo := new(corev1.Pod)
	err := ocphealthcheckutil.ConvertUnStructureToStructured(newObj, newPo)
	if err != nil {
		log.Log.Error(err, "failed to convert")
		return
	}
	if newPo.DeletionTimestamp != nil {
		// assuming it is deletion, so ignoring
		return
	}
	if sameNs, err := IsChildPolicyNamespace(clientset, newPo.Namespace); err != nil {
		log.Log.Info("unable to retrieve policy object namespace")
		return
	} else if sameNs {
		SendEmail(fmt.Sprintf("cgu update in progress for namespace %s", newPo.Namespace), "faulty", fmt.Sprintf("possible CGU update is in progress for objects in namespace %s in cluster %s, no pod update alerts will be sent until CGU is compliant, please execute <oc get pods -n %s and oc get policy -A> to validate", newPo.Namespace, runningHost, newPo.Namespace), runningHost, spec)
		return
	} else {
		SendEmail(fmt.Sprintf("/home/golanguser/files/.%s-%s.txt", "cgu", newPo.Namespace), "recovered", fmt.Sprintf("Appears that CGU update is completed for objects in namespace %s in cluster %s, pod update alerts will continue to be sent", newPo.Namespace, runningHost), runningHost, spec)
	}
	for _, newCont := range newPo.Status.ContainerStatuses {
		if newCont.State.Terminated != nil && newCont.State.Terminated.ExitCode != 0 {
			SendEmail(fmt.Sprintf("/home/golanguser/files/.%s-%s.txt", newPo.Name, newPo.Namespace), "faulty", fmt.Sprintf("pod %s's container %s is terminated with non exit code 0 in namespace %s in cluster %s", newPo.Name, newCont.Name, newPo.Namespace, runningHost), runningHost, spec)
		} else if newCont.State.Running != nil || (newCont.State.Terminated != nil && newCont.State.Terminated.ExitCode == 0) {
			// Assuming if pod has moved back to running from CrashLoopBackOff/others, the restart count will always be greater than 0
			if newCont.RestartCount > 0 {
				if newCont.LastTerminationState.Terminated != nil && newCont.LastTerminationState.Terminated.ExitCode != 0 && newCont.LastTerminationState.Terminated.FinishedAt.String() != "" {
					if podLastRestartTimerUp(newCont.LastTerminationState.Terminated.FinishedAt.String()) {
						SendEmail(fmt.Sprintf("/home/golanguser/files/.%s-%s.txt", newPo.Name, newPo.Namespace), "faulty", fmt.Sprintf("pod %s's container %s whic was previously terminate with non exit code 0 is now either running/completed in namespace %s in cluster %s", newPo.Name, newCont.Name, newPo.Namespace, runningHost), runningHost, spec)
					}
				}
			}
		}
		if newCont.State.Waiting != nil {
			if newCont.State.Waiting.Reason == "CrashLoopBackOff" {
				if len(newPo.Spec.Volumes) > 0 {
					for _, vol := range newPo.Spec.Volumes {
						if vol.PersistentVolumeClaim != nil && vol.PersistentVolumeClaim.ClaimName != "" {
							// Get the persistent volume claim name
							pvc, err := clientset.CoreV1().PersistentVolumeClaims(newPo.Namespace).Get(context.Background(), vol.PersistentVolumeClaim.ClaimName, metav1.GetOptions{})
							if err != nil {
								if k8serrors.IsNotFound(err) {
									SendEmail(fmt.Sprintf("/home/golanguser/files/.%s-%s.txt", newPo, newPo.Namespace), "faulty", fmt.Sprintf("Pod %s's container %s is in CrashLoopBackOff state, configured PVC %s doesn't exist in namespace %s in cluster %s, please execute <oc get pods %s -n %s -o json | jq .spec.volumes[] and oc get pvc %s -n %s -o json | jq .spec.volumeName> to validate it", newPo.Name, newCont.Name, pvc.Name, pvc.Namespace, runningHost, newPo.Name, newPo.Namespace, pvc.Name, pvc.Namespace), runningHost, spec)
								} else {
									SendEmail(fmt.Sprintf("/home/golanguser/files/.%s-%s.txt", newPo, newPo.Namespace), "faulty", fmt.Sprintf("Pod %s's container %s is in CrashLoopBackOff state, unable to retrieve configured PVC %s in namespace %s in cluster %s, please execute <oc get pods %s -n %s -o json | jq .spec.volumes[] and oc get pvc %s -n %s -o json | jq .spec.volumeName> to validate it", newPo.Name, newCont.Name, pvc.Name, pvc.Namespace, runningHost, newPo.Name, newPo.Namespace, pvc.Name, pvc.Namespace), runningHost, spec)
								}
							}
							if affected, err := ocphealthcheckutil.PvHasDifferentNode(clientset, pvc.Spec.VolumeName, newPo.Spec.NodeName); err != nil {
								SendEmail(fmt.Sprintf("/home/golanguser/files/.%s-%s.txt", newPo, newPo.Namespace), "faulty", fmt.Sprintf("Pod %s's container %s is in CrashLoopBackOff state, unable to retrieve volume attachment of volume %s in cluster %s, please execute <oc get pods %s -n %s -o json | jq .status.containerStatuses and oc get volumeattachments | grep %s > to validate it", newPo.Name, newCont.Name, pvc.Spec.VolumeName, runningHost, newPo, newPo.Namespace, pvc.Spec.VolumeName), runningHost, spec)
							} else if err == nil && affected {
								SendEmail(fmt.Sprintf("/home/golanguser/files/.%s-%s.txt", newPo, newPo.Namespace), "faulty", fmt.Sprintf("Pod %s's container %s is in CrashLoopBackOff state, volume attachment of volume %s is mounted on a different node in cluster %s, please execute <oc get pods %s -n %s -o json | jq .status.containerStatuses and oc get volumeattachments | grep %s > to validate it", newPo.Name, newCont.Name, pvc.Spec.VolumeName, runningHost, newPo, newPo.Namespace, pvc.Spec.VolumeName), runningHost, spec)
							} else {
								// Check if it is due to other issues
								for _, cont := range newPo.Spec.Containers {
									if cont.Name == newCont.Name {
										if newCont.Image != cont.Image {
											SendEmail(fmt.Sprintf("/home/golanguser/files/.%s-%s.txt", newPo, newPo.Namespace), "faulty", fmt.Sprintf("Pod %s's container %s is in CrashLoopBackOff state, volume attachment of volume %s is mounted on the SAME node, could be other issues in cluster %s, please execute <oc get pods %s -n %s -o json | jq .status.containerStatuses and oc get volumeattachments | grep %s > to validate it", newPo.Name, newCont.Name, pvc.Spec.VolumeName, runningHost, newPo, newPo.Namespace, pvc.Spec.VolumeName), runningHost, spec)
										}
									}
								}
							}
						} else {
							for _, cont := range newPo.Spec.Containers {
								if cont.Name == newCont.Name {
									if newCont.Image != cont.Image {
										SendEmail(fmt.Sprintf("/home/golanguser/files/.%s-%s.txt", newPo, newPo.Namespace), "faulty", fmt.Sprintf("Pod %s's container %s is in CrashLoopBackOff state, appears to be ErrImagePull error in cluster %s, please execute <oc get pods %s -n %s -o json | jq .status.containerStatuses and oc  > to validate it", newPo.Name, newCont.Name, runningHost, newPo, newPo.Namespace), runningHost, spec)
									} else {
										SendEmail(fmt.Sprintf("/home/golanguser/files/.%s-%s.txt", newPo.Name, newPo.Namespace), "faulty", fmt.Sprintf("Pod %s's container %s is in CrashLoopBackOff state, no persistent volume is attached to the pod, doesn't seem to be ErrImagePull, could be other issues, in cluster %s, please execute <oc get pods %s -n %s -o json | jq .status.containerStatuses> to validate it", newPo.Name, newCont.Name, runningHost, newPo.Name, newPo.Namespace), runningHost, spec)
									}
								}
							}
						}
					}
				}
			} else if newCont.State.Waiting.Reason == "ErrImagePull" {
				SendEmail(fmt.Sprintf("/home/golanguser/files/.%s-%s.txt", newPo.Name, newPo.Namespace), "faulty", fmt.Sprintf("pod %s's container %s is failing in namespace %s due to ErrImagePull in cluster %s", newPo.Name, newCont.Name, newPo.Namespace, runningHost), runningHost, spec)
			}
		} else {
			// Assuming if pod has moved back to running from CrashLoopBackOff/others, the restart count will always be greater than 0
			if newCont.RestartCount > 0 {
				if newCont.LastTerminationState.Terminated != nil && newCont.LastTerminationState.Terminated.ExitCode != 0 && newCont.LastTerminationState.Terminated.FinishedAt.String() != "" {
					if podLastRestartTimerUp(newCont.LastTerminationState.Terminated.FinishedAt.String()) {
						SendEmail(fmt.Sprintf("/home/golanguser/files/.%s-%s.txt", newPo.Name, newPo.Namespace), "recovered", fmt.Sprintf("pod %s's container %s which was previously terminated with non exit code 0 is now either running/completed in namespace %s in cluster %s ", newPo.Name, newCont.Name, newPo.Namespace, runningHost), runningHost, spec)
					}
				} else if newCont.LastTerminationState.Terminated != nil && newCont.LastTerminationState.Terminated.ExitCode == 0 {
					// this condition will satisfy the pod that was previously running/completed and went into issues (due to image pull for example) and becomes running/completed
					SendEmail(fmt.Sprintf("/home/golanguser/files/.%s-%s.txt", newPo.Name, newPo.Namespace), "recovered", fmt.Sprintf("pod %s's container %s which was previously terminated with non exit code 0 is now either running/completed in namespace %s in cluster %s ", newPo.Name, newCont.Name, newPo.Namespace, runningHost), runningHost, spec)
				}
			} else {
				// this condition will satisfy the pod that was never in running state (due to image pull for example) and becomes running/completed
				if newCont.LastTerminationState.Terminated == nil {
					SendEmail(fmt.Sprintf("/home/golanguser/files/.%s-%s.txt", newPo.Name, newPo.Namespace), "recovered", fmt.Sprintf("pod %s's container %s which was previously terminated/waiting is now either running/completed in namespace %s in cluster %s ", newPo.Name, newCont.Name, newPo.Namespace, runningHost), runningHost, spec)
				}
			}
		}
	}
}

func CleanUpRunningPods(clientset *kubernetes.Clientset, spec *ocphealthcheckv1.OcpHealthCheckSpec, status *ocphealthcheckv1.OcpHealthCheckStatus, runningHost string) {
	podList, err := clientset.CoreV1().Pods(corev1.NamespaceAll).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		log.Log.Info(fmt.Sprintf("unable to retrieve pods due to error %s", err.Error()))
	}
	if files, err := os.ReadDir("/home/golanguser/files/"); err != nil {
		log.Log.Info(err.Error())
	} else {
		for _, file := range files {
			if len(podList.Items) > 0 {
				for _, pod := range podList.Items {
					failingContainers := []string{}
					timersUp := []bool{}
					if strings.Contains(file.Name(), fmt.Sprintf(".%s-%s.txt", pod.Name, pod.Namespace)) {
						for _, cont := range pod.Status.ContainerStatuses {
							if cont.State.Running == nil {
								failingContainers = append(failingContainers, cont.Name)
							} else {
								if cont.RestartCount < 1 {
									if cont.LastTerminationState.Terminated != nil && cont.LastTerminationState.Terminated.ExitCode != 0 && cont.LastTerminationState.Terminated.FinishedAt.String() != "" {
										if !podLastRestartTimerUp(cont.LastTerminationState.Terminated.FinishedAt.String()) {
											timersUp = append(timersUp, false)
										}
									}
								}

							}
						}
					}
					if len(failingContainers) < 1 && len(timersUp) < 1 {
						SendEmail(fmt.Sprintf("/home/golanguser/files/.%s-%s.txt", pod.Name, pod.Namespace), "recovered", fmt.Sprintf("pod %s which was previously waiting/terminated with non exit code 0 is now either running/completed in namespace %s in cluster %s ", pod.Name, pod.Namespace, runningHost), runningHost, spec)
					}
				}
			}
		}
	}
}

func deleteOCPElementSlice(slice []string, index int) []string {
	return append(slice[:index], slice[index+1:]...)
}

func OnNodeUpdate(newObj interface{}, spec *ocphealthcheckv1.OcpHealthCheckSpec, status *ocphealthcheckv1.OcpHealthCheckStatus, runningHost string, mcp *ocphealthcheckutil.MCPStruct) {
	node := new(corev1.Node)
	err := ocphealthcheckutil.ConvertUnStructureToStructured(newObj, node)
	if err != nil {
		log.Log.Error(err, "failed to convert")
		return
	}
	if node.DeletionTimestamp != nil {
		//assuming deletion, ignoring it
		return
	}
	for anno, val := range node.Annotations {
		// to be updated
		if anno == "machineconfiguration.openshift.io/state" {
			if val != MACHINECONFIGUPDATEDONE {
				// assuming mcp update is in progress, check and report if it is failing
				if val == MACHINECONFIGUPDATEINPROGRESS || val == MACHINECONFIGUPDATEREBOOTING {
					// assuming mcp update is progressing without issues
					return
				} else if val == MACHINECONFIGUPDATEDEGRADED || val == MACHINECONFIGUPDATEUNRECONCILABLE {
					// assuming mcp udpate ran into issues and report
					SendEmail(fmt.Sprintf("/home/golanguser/files/.%s-%s.txt", node.Name, "mcp-issue"), "faulty", fmt.Sprintf("node %s's mcp update is either degraded/unreconcilable in cluster %s, please execute <oc describe node %s> to validate it", node.Name, runningHost, node.Name), runningHost, spec)
					return
				}
			} else {
				SendEmail(fmt.Sprintf("/home/golanguser/files/.%s-%s.txt", node.Name, "mcp-issue"), "recovered", fmt.Sprintf("node %s's mcp update which was previously degraded/unreconcilable is now done in cluster %s", node.Name, runningHost), runningHost, spec)
			}
		}
	}
	if node.Spec.Unschedulable {
		SendEmail(fmt.Sprintf("/home/golanguser/files/.%s-%s.txt", node.Name, "sched"), "faulty", fmt.Sprintf("node %s has become unschedulable (No MCP update is in progress) in cluster %s, please execute <oc get nodes %s > to validate it", node.Name, runningHost, node.Name), runningHost, spec)
	} else {
		SendEmail(fmt.Sprintf("/home/golanguser/files/.%s-%s.txt", node.Name, "sched"), "recovered", fmt.Sprintf("node %s which was previously unschedulable is now schedulable again in cluster %s", node.Name, runningHost), runningHost, spec)
	}
	for _, cond := range node.Status.Conditions {
		if cond.Type == "Ready" && cond.Status == "False" {
			SendEmail(fmt.Sprintf("/home/golanguser/files/.%s-%s.txt", node.Name, "ready"), "faulty", fmt.Sprintf("node %s has become NotReady (No MCP update is in progress) in cluster %s, please execute <oc get nodes %s > to validate it", node.Name, runningHost, node.Name), runningHost, spec)
		} else if cond.Type == "Ready" && cond.Status == "True" {
			SendEmail(fmt.Sprintf("/home/golanguser/files/.%s-%s.txt", node.Name, "ready"), "recovered", fmt.Sprintf("node %s which was previously marked as NotReady in cluster %s, please execute <oc get nodes %s > to validate it", node.Name, runningHost, node.Name), runningHost, spec)
		}
	}
}

func OnPolicyUpdate(newObj interface{}, spec *ocphealthcheckv1.OcpHealthCheckSpec, status *ocphealthcheckv1.OcpHealthCheckStatus, runningHost string) {
	policy := new(ocmpolicy.Policy)
	err := ocphealthcheckutil.ConvertUnStructureToStructured(newObj, policy)
	if err != nil {
		log.Log.Error(err, "failed to convert")
		return
	}
	if policy.DeletionTimestamp != nil {
		// assuming it is deletion, so will ignore it
		return
	}
	if !IsChildPolicy(policy) {
		if policy.Status.ComplianceState == "NonCompliant" || policy.Spec.Disabled {
			SendEmail(fmt.Sprintf("/home/golanguser/files/.%s-%s.txt", policy.Name, "noncomplaint"), "faulty", fmt.Sprintf("policy %s is either non-compliant/disabled in namespace %s in cluster %s, please execute <oc get policy %s -n %s -o json | jq .status> to validate it", policy.Name, policy.Namespace, runningHost, policy.Name, policy.Namespace), runningHost, spec)
		} else {
			SendEmail(fmt.Sprintf("/home/golanguser/files/.%s-%s.txt", policy.Name, "noncomplaint"), "recovered", fmt.Sprintf("policy %s which was previously non-compliant/disabled is now compliant/enabled again in namespace %s in cluster %s", policy.Name, policy.Namespace, runningHost), runningHost, spec)
		}
	}
}

func OnPolicyDelete(oldObj interface{}, spec *ocphealthcheckv1.OcpHealthCheckSpec, status *ocphealthcheckv1.OcpHealthCheckStatus, runningHost string) {
	policy := new(ocmpolicy.Policy)
	err := ocphealthcheckutil.ConvertUnStructureToStructured(oldObj, policy)
	if err != nil {
		log.Log.Error(err, "failed to convert")
		return
	}
	SendEmail(fmt.Sprintf("/home/golanguser/files/.%s-%s.txt", policy.Name, "noncomplaint"), "recovered", fmt.Sprintf("policy %s which was previously non-compliant/disabled is now deleted in namespace %s in cluster %s", policy.Name, policy.Namespace, runningHost), runningHost, spec)
}

func IsChildPolicy(policy *ocmpolicy.Policy) bool {
	for labelName, _ := range policy.Labels {
		if labelName == "openshift-cluster-group-upgrades/parentPolicyName" {
			return true
		}
	}
	return false
}

func IsChildPolicyNamespace(clientset *kubernetes.Clientset, ns string) (bool, error) {
	policyNamespace, err := GetChildPolicyObjectNamespace(clientset)
	if err != nil {
		return false, err
	}
	if policyNamespace != "" && policyNamespace == ns {
		return true, nil
	}
	return false, nil
}

func GetChildPolicyObjectNamespace(clientset *kubernetes.Clientset) (string, error) {
	policies := ocmpolicy.PolicyList{}
	err := clientset.RESTClient().Get().AbsPath("/apis/policy.open-cluster-management.io/v1/policies").Do(context.Background()).Into(&policies)
	if err != nil {
		return "", err
	}
	if len(policies.Items) > 0 {
		for _, policy := range policies.Items {
			if IsChildPolicy(&policy) {
				if policy.Name == "ztp-cwl.storage-netapp-trident" {
					for _, temp := range policy.Spec.PolicyTemplates {
						obj, _, err := unstructured.UnstructuredJSONScheme.Decode(temp.ObjectDefinition.Raw, nil, nil)
						if err != nil {
							return "", err
						}

						unObj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
						if err != nil {
							return "", err
						}
						var objNamespace string
						for _, val := range unObj {
							if v2, ok := val.(map[string]interface{}); ok {
								for k3, v3 := range v2 {
									if k3 == "object-templates" {
										for _, v4 := range v3.([]interface{}) {
											if v5, ok := v4.(map[string]interface{}); ok {
												for k6, v6 := range v5 {
													if k6 == "objectDefinition" {
														for k7, v7 := range v6.(map[string]interface{}) {
															if k7 == "metadata" {
																for k8, v8 := range v7.(map[string]interface{}) {
																	if k8 == "namespace" {
																		if v8.(string) != "" {
																			objNamespace = v8.(string)
																			return objNamespace, nil
																		}
																	}
																}
															}
														}
													}
												}
											}
										}
									}
								}
							}
						}
					}
				}
			}
		}
	}
	return "", nil
}

func OnPolicyAdd(newObj interface{}, spec *ocphealthcheckv1.OcpHealthCheckSpec, status *ocphealthcheckv1.OcpHealthCheckStatus) {
	policy := new(ocmpolicy.Policy)
	err := ocphealthcheckutil.ConvertUnStructureToStructured(newObj, policy)
	if err != nil {
		log.Log.Error(err, "failed to convert")
		return
	}
	if !IsChildPolicy(policy) {
		log.Log.Info(fmt.Sprintf("New policy.open-cluster-management.io/v1 %s has been added to namespace %s", policy.Name, policy.Namespace))
	} else {
		log.Log.Info(fmt.Sprintf("New child policy %s has been added to namespace %s, possible CGU update is in progress, please check HUB cluster", policy.Name, policy.Namespace))
	}
}

func OnPodDelete(oldObj interface{}, spec *ocphealthcheckv1.OcpHealthCheckSpec, status *ocphealthcheckv1.OcpHealthCheckStatus, runningHost string) {
	po := new(corev1.Pod)
	err := ocphealthcheckutil.ConvertUnStructureToStructured(oldObj, po)
	if err != nil {
		log.Log.Error(err, "failed to convert")
		return
	}
	SendEmail(fmt.Sprintf("/home/golanguser/files/.%s-%s.txt", po.Name, po.Namespace), "faulty", fmt.Sprintf("pod %s's container which was previously terminated/CrashLoopBackOff is now deleted in namespace %s in cluster %s ", po.Name, po.Namespace, runningHost), runningHost, spec)
}

func SendEmail(filename string, alertType string, alertString string, runningHost string, spec *ocphealthcheckv1.OcpHealthCheckSpec) {
	if spec.SuspendEmailAlert != nil && !*spec.SuspendEmailAlert {
		if alertType == "faulty" {
			ocphealthcheckutil.SendEmailAlert(runningHost, filename, spec, alertString)
		} else {
			ocphealthcheckutil.SendEmailRecoveredAlert(runningHost, filename, spec, alertString)
		}
	}
}

func OnCoUpdate(newObj interface{}, clientset *kubernetes.Clientset, spec *ocphealthcheckv1.OcpHealthCheckSpec, runningHost string) {
	co := new(cov1.ClusterOperator)
	err := ocphealthcheckutil.ConvertUnStructureToStructured(newObj, co)
	if err != nil {
		log.Log.Error(err, "failed to convert")
		return
	}
	if co.DeletionTimestamp != nil {
		return
	}
	// check if actual mcp is in progress
	if mcp, err := CheckMCPINProgress(clientset); err != nil {
		return
	} else if err == nil && mcp {
		return
	}

	for _, cond := range co.Status.Conditions {
		if cond.Type == "Degraded" {
			if cond.Status == "True" {
				SendEmail(fmt.Sprintf("/home/golanguser/files/.%s.txt", co.Name), "faulty", fmt.Sprintf("Cluster operator %s is degraded and no actual mcp update is in progress in cluster %s, please execute <oc get co> to validate it", co.Name, runningHost), runningHost, spec)
			} else {
				SendEmail(fmt.Sprintf("/home/golanguser/files/.%s.txt", co.Name), "recovered", fmt.Sprintf("Cluster operator %s which was previously degraded is back to working state in cluster %s, please execute <oc get co> to validate it", co.Name, runningHost), runningHost, spec)
			}
		}
	}
}

func OnNNCPUpdate(newObj interface{}, clientset *kubernetes.Clientset, spec *ocphealthcheckv1.OcpHealthCheckSpec, runningHost string) {
	nncp := new(nmstate.NodeNetworkConfigurationPolicy)
	err := ocphealthcheckutil.ConvertUnStructureToStructured(newObj, nncp)
	if err != nil {
		log.Log.Error(err, "failed to convert")
		return
	}
	if nncp.DeletionTimestamp != nil {
		return
	}
	if mcp, err := CheckMCPINProgress(clientset); err != nil {
		return
	} else if err == nil && mcp {
		return
	}

	for _, cond := range nncp.Status.Conditions {
		if cond.Type == "Degraded" {
			if cond.Status == "True" {
				SendEmail(fmt.Sprintf("/home/golanguser/files/.%s.txt", nncp.Name), "faulty", fmt.Sprintf("NNCP %s is degraded and no actual mcp update is in progress in cluster %s, please execute <oc get nncp> to validate it", nncp.Name, runningHost), runningHost, spec)
			} else {
				SendEmail(fmt.Sprintf("/home/golanguser/files/.%s.txt", nncp.Name), "recovered", fmt.Sprintf("NNCP %s which was previously degraded is back to working state in cluster %s, please execute <oc get nncp> to validate it", nncp.Name, runningHost), runningHost, spec)
			}
		}
	}
}

func OnCatalogSourceUpdate(newObj interface{}, clientset *kubernetes.Clientset, spec *ocphealthcheckv1.OcpHealthCheckSpec, runningHost string) {
	cs := new(operatorframework.CatalogSource)
	err := ocphealthcheckutil.ConvertUnStructureToStructured(newObj, cs)
	if err != nil {
		log.Log.Error(err, "failed to convert")
		return
	}
	if cs.DeletionTimestamp != nil {
		return
	}
	if mcp, err := CheckMCPINProgress(clientset); err != nil {
		return
	} else if err == nil && mcp {
		return
	}
	if cs.Status.GRPCConnectionState.LastObservedState != "READY" {
		SendEmail(fmt.Sprintf("/home/golanguser/files/.%s.txt", cs.Name), "faulty", fmt.Sprintf("CatalogSource %s's connection state is not READY and no actual mcp update is in progress in cluster %s, please execute <oc get catalogsources> to validate it", cs.Name, runningHost), runningHost, spec)
	} else {
		SendEmail(fmt.Sprintf("/home/golanguser/files/.%s.txt", cs.Name), "recovered", fmt.Sprintf("CatalogSource %s's connection state which was previously NOTREADY is READY now in cluster %s, please execute <oc get catalogsources> to validate it", cs.Name, runningHost), runningHost, spec)
	}
}

func OnCsvUpdate(newObj interface{}, clientset *kubernetes.Clientset, spec *ocphealthcheckv1.OcpHealthCheckSpec, runningHost string) {
	cs := new(operatorframework.ClusterServiceVersion)
	err := ocphealthcheckutil.ConvertUnStructureToStructured(newObj, cs)
	if err != nil {
		log.Log.Error(err, "failed to convert")
		return
	}
	if cs.DeletionTimestamp != nil {
		return
	}
	if mcp, err := CheckMCPINProgress(clientset); err != nil {
		return
	} else if err == nil && mcp {
		return
	}
	if pol, err := IsChildPolicyNamespace(clientset, cs.Namespace); err != nil {
		return
	} else if err == nil && pol {
		return
	}

	if cs.Status.Phase != "Succeeded" {
		SendEmail(fmt.Sprintf("/home/golanguser/files/.%s.txt", cs.Name), "faulty", fmt.Sprintf("CSV %s is either degraded/in-progress in namespace %s and no actual mcp update is in progress in cluster %s, please execute <oc get csv -n %s> to validate it", cs.Name, cs.Namespace, runningHost, cs.Namespace), runningHost, spec)
	} else {
		SendEmail(fmt.Sprintf("/home/golanguser/files/.%s.txt", cs.Name), "recovered", fmt.Sprintf("CSV %s which was previously degraded/in-progress is succeeded now in namespace %s in cluster %s, please execute <oc get csv -n %s> to validate it", cs.Name, cs.Namespace, runningHost, cs.Namespace), runningHost, spec)
	}
}

func CheckMCPINProgress(clientset *kubernetes.Clientset) (bool, error) {
	mcpList := mcfgv1.MachineConfigPoolList{}
	err := clientset.RESTClient().Get().AbsPath("/apis/machineconfiguration.openshift.io/v1/machineconfigpools").Do(context.Background()).Into(&mcpList)
	if err != nil {
		return false, err
	}
	for _, mcp := range mcpList.Items {
		for _, cond := range mcp.Status.Conditions {
			if cond.Type == "Updating" {
				if cond.Status == "True" {
					if mcpInProgress, _, err := ocphealthcheckutil.CheckNodeMcpAnnotations(clientset, mcp.Spec.NodeSelector.MatchLabels); err != nil {
						return false, err
					} else if err == nil && mcpInProgress {
						return true, nil
					}
					// ignored else condition as mcp update true condition could have caused by manual update
				}
			}
		}
	}
	return false, nil
}
