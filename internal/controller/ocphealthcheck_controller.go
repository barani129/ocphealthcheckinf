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
	"slices"
	"strings"
	"sync"
	"time"

	ocphealthcheckv1 "github.com/barani129/ocphealthcheckinf/api/v1"
	ocphealthcheckutil "github.com/barani129/ocphealthcheckinf/internal/util"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
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
}

const (
	MACHINECONFIGUPDATEDONE       = "Done"
	MACHINECONFIGUPDATEDEGRADED   = "Degraded"
	MACHINECONFIGUPDATEINPROGRESS = "Working"
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

	if len(status.FailedResources) > 0 {
		status.FailedChecks = append(status.FailedChecks, status.FailedResources...)
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
		status.FailedResources = append(status.FailedResources, status.FailedChecks...)
		if len(status.FailedResources) > 0 {
			status.Healthy = true
		} else {
			status.Healthy = false
		}
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

	// var defaultHealthCheckInterval time.Duration
	// if spec.CheckInterval != nil {
	// 	defaultHealthCheckInterval = time.Minute * time.Duration(*spec.CheckInterval)
	// } else {
	// 	defaultHealthCheckInterval = time.Minute * 30
	// }

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

	b := false
	mcpParam := ocphealthcheckutil.MCPStruct{
		IsMCPInProgress: &b,
		IsNodeAffected:  &b,
		MCPAnnoNode:     "",
		MCPNode:         "",
		MCPAnnoState:    "",
		MCPNodeState:    "",
	}
	log.Log.Info("Starting dynamic informer factory")
	nsFactory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(clientset, time.Minute, corev1.NamespaceAll, nil)
	mcpInformer := nsFactory.ForResource(mcpResource).Informer()
	podInformer := nsFactory.ForResource(podResource).Informer()
	nodeInformer := nsFactory.ForResource(nodeResource).Informer()
	policyInformer := nsFactory.ForResource(policyResource).Informer()

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
			err := onMCPUpdate(newObj, staticClientSet, &mcpParam, status, spec, runningHost)
			if err != nil {
				log.Log.Info("It is possible that actual MachineConfigPool is in progress/unable to retrieve the APIs")
				return
			}
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
	// go podInformer.Run(context.Background().Done())
	nsFactory.Start(context.Background().Done())
	// TO DO:
	// NNCP, CO, Sub
	log.Log.Info("Waiting for cache sync")
	isSynced := cache.WaitForCacheSync(context.Background().Done(), podInformer.HasSynced, nodeInformer.HasSynced, mcpInformer.HasSynced, policyInformer.HasSynced)
	mux.Lock()
	synced = isSynced
	mux.Unlock()
	log.Log.Info("cache sync is completed")
	if !isSynced {
		return ctrl.Result{}, fmt.Errorf("failed to sync")
	}
	report(ocphealthcheckv1.ConditionTrue, "pod informers compiled successfully", nil)
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

func onMCPUpdate(newObj interface{}, staticClientSet *kubernetes.Clientset, mcpParam *ocphealthcheckutil.MCPStruct, status *ocphealthcheckv1.OcpHealthCheckStatus, spec *ocphealthcheckv1.OcpHealthCheckSpec, runningHost string) error {
	mcp := new(mcfgv1.MachineConfigPool)
	err := ocphealthcheckutil.ConvertUnStructureToStructured(newObj, mcp)
	if err != nil {
		log.Log.Error(err, "failed to convert")
		return err
	}
	if spec.HubCluster != nil && *spec.HubCluster {
		if mcp.Spec.Paused {
			if !slices.Contains(status.FailedChecks, fmt.Sprintf("mcp %s is paused", mcp.Name)) {
				if mcpParam.IsMCPInProgress != nil && *mcpParam.IsMCPInProgress {
					if mcpParam.MCPAnnoNode != "" && mcpParam.MCPAnnoState != "" {
						log.Log.Info(fmt.Sprintf("MachineConfig %s paused and actual update is in progress and node %s's annotation has been set to state %s", mcp.Name, mcpParam.MCPAnnoNode, mcpParam.MCPAnnoState))
						status.FailedChecks = append(status.FailedChecks, fmt.Sprintf("mcp %s is paused", mcp.Name))
						if spec.SuspendEmailAlert != nil && !*spec.SuspendEmailAlert {
							ocphealthcheckutil.SendEmailAlert(runningHost, fmt.Sprintf("/home/golanguser/.%s-%s.txt", "mcp-pause", mcp.Name), spec, fmt.Sprintf("MachineConfig pool %s is paused and actual update is in progress and node %s's annotation has been set to state %s in cluster %s, please execute <oc get mcp %s > to validate it", mcp.Name, mcpParam.MCPAnnoNode, mcpParam.MCPAnnoState, runningHost, mcp.Name))
						}
					}
				} else {
					log.Log.Info(fmt.Sprintf("MachineConfig %s paused and update is not in progress", mcp.Name))
					status.FailedChecks = append(status.FailedChecks, fmt.Sprintf("mcp %s is paused", mcp.Name))
					if spec.SuspendEmailAlert != nil && !*spec.SuspendEmailAlert {
						ocphealthcheckutil.SendEmailAlert(runningHost, fmt.Sprintf("/home/golanguser/.%s-%s.txt", "mcp-pause", mcp.Name), spec, fmt.Sprintf("MachineConfig pool %s is paused and update is not in progress", mcp.Name))
					}
				}
			}
		} else {
			if slices.Contains(status.FailedChecks, fmt.Sprintf("mcp %s is paused", mcp.Name)) {
				log.Log.Info(fmt.Sprintf("MachineConfig %s paused and update is not in progress", mcp.Name))
				idx := slices.Index(status.FailedChecks, fmt.Sprintf("mcp %s is paused", mcp.Name))
				if len(status.FailedChecks) == 1 {
					status.FailedChecks = nil
				} else {
					status.FailedChecks = deleteOCPElementSlice(status.FailedChecks, idx)
				}
				if spec.SuspendEmailAlert != nil && !*spec.SuspendEmailAlert {
					ocphealthcheckutil.SendEmailAlert(runningHost, fmt.Sprintf("/home/golanguser/.%s-%s.txt", "mcp-pause", mcp.Name), spec, fmt.Sprintf("MachineConfig pool %s which was previously paused is now unpaused", mcp.Name))
				}
				os.Remove(fmt.Sprintf("/home/golanguser/.%s-%s.txt", "mcp-pause", mcp.Name))
			}
		}
	}

	for _, cond := range mcp.Status.Conditions {
		if cond.Type == "Updating" {
			if cond.Status == "True" {
				// Check node annotations to validate it
				if mcp.Spec.MachineConfigSelector.MatchLabels != nil {
					// err := ocphealthcheckutil.CheckNodeMcpAnnotations(staticClientSet, mcp.Spec.NodeSelector.MatchLabels, mcpParam)
					// if err != nil {
					// 	log.Log.Error(err, "unable to check node MCP annotations")
					// 	return err
					// }
					if mcpParam.IsMCPInProgress != nil && *mcpParam.IsMCPInProgress {
						if mcpParam.MCPAnnoNode != "" && mcpParam.MCPAnnoState != "" {
							if !slices.Contains(status.FailedChecks, fmt.Sprintf("mcp %s update is in progress", mcp.Name)) {
								log.Log.Info(fmt.Sprintf("MachineConfig %s update is in progress and node %s's annotation has been set to state %s", mcp.Name, mcpParam.MCPAnnoNode, mcpParam.MCPAnnoState))
								status.FailedChecks = append(status.FailedChecks, fmt.Sprintf("mcp %s update is in progress", mcp.Name))
								if spec.SuspendEmailAlert != nil && !*spec.SuspendEmailAlert {
									ocphealthcheckutil.SendEmailAlert(runningHost, fmt.Sprintf("/home/golanguser/.%s-%s.txt", "mcp", mcp.Name), spec, fmt.Sprintf("MachineConfig pool %s update is in progress and node %s's annotation has been set to state %s in cluster %s, please execute <oc get mcp %s > to validate it", mcp.Name, mcpParam.MCPAnnoNode, mcpParam.MCPAnnoState, runningHost, mcp.Name))
								}
							}
						}
						return fmt.Errorf("actual mcp update is in progress")
					} else {
						if mcpParam.IsNodeAffected != nil && *mcpParam.IsNodeAffected {
							if mcpParam.MCPNode != "" && mcpParam.MCPNodeState != "" {
								if !slices.Contains(status.FailedChecks, fmt.Sprintf("mcp %s update is in progress", mcp.Name)) {
									log.Log.Info(fmt.Sprintf("machineconfig pool %s update is in progress, node %s has become %s, possible manual action", mcp.Name, mcpParam.MCPNode, mcpParam.MCPNodeState))
									status.FailedChecks = append(status.FailedChecks, fmt.Sprintf("mcp %s update is in progress", mcp.Name))
									if spec.SuspendEmailAlert != nil && !*spec.SuspendEmailAlert {
										ocphealthcheckutil.SendEmailAlert(runningHost, fmt.Sprintf("/home/golanguser/.%s-%s.txt", "mcp", mcp.Name), spec, fmt.Sprintf("MachineConfig pool %s update has been set to true, node %s has become %s, possible manual action in cluster %s, please execute <oc get mcp %s and oc get nodes to validate it", mcp.Name, mcpParam.MCPNode, mcpParam.MCPNodeState, runningHost, mcp.Name))
									}
								}
							}
						} else {
							if !slices.Contains(status.FailedChecks, fmt.Sprintf("mcp %s update is in progress", mcp.Name)) {
								log.Log.Info(fmt.Sprintf("machineconfig pool %s update is in progress, but nodes are healthy and schedulable", mcp.Name))
								status.FailedChecks = append(status.FailedChecks, fmt.Sprintf("mcp %s update is in progress", mcp.Name))
								if spec.SuspendEmailAlert != nil && !*spec.SuspendEmailAlert {
									ocphealthcheckutil.SendEmailAlert(runningHost, fmt.Sprintf("/home/golanguser/.%s-%s.txt", "mcp", mcp.Name), spec, fmt.Sprintf("MachineConfig pool %s update has been set to true, but nodes are healthy and schedulable, mcp is probably just starting now, in cluster %s, please execute <oc get mcp %s and oc get nodes> to validate it", mcp.Name, runningHost, mcp.Name))
								}
							}
						}
					}
				}
			} else if cond.Status == "False" {
				ocphealthcheckutil.DisableMCPAnno(mcpParam)
				if slices.Contains(status.FailedChecks, fmt.Sprintf("mcp %s update is in progress", mcp.Name)) {
					if spec.SuspendEmailAlert != nil && !*spec.SuspendEmailAlert {
						ocphealthcheckutil.SendEmailRecoveredAlert(runningHost, fmt.Sprintf("/home/golanguser/.%s-%s.txt", "mcp", mcp.Name), spec, fmt.Sprintf("MachineConfig pool %s update is no longer in progress in cluster %s, please execute <oc get mcp %s > to validate it", mcp.Name, runningHost, mcp.Name))
					}
					idx := slices.Index(status.FailedChecks, fmt.Sprintf("mcp %s update is in progress", mcp.Name))
					if len(status.FailedChecks) == 1 {
						status.FailedChecks = nil
					} else {
						status.FailedChecks = deleteOCPElementSlice(status.FailedChecks, idx)
					}
					os.Remove(fmt.Sprintf("/home/golanguser/.%s-%s.txt", "mcp", mcp.Name))
				}
			}
		} else if cond.Type == "Degraded" {
			if cond.Status == "True" {
				if !slices.Contains(status.FailedChecks, fmt.Sprintf("mcp %s is degraded", mcp.Name)) {
					log.Log.Info(fmt.Sprintf("machineconfig pool %s is degraded", mcp.Name))
					status.FailedChecks = append(status.FailedChecks, fmt.Sprintf("mcp %s is degraded", mcp.Name))
					if spec.SuspendEmailAlert != nil && !*spec.SuspendEmailAlert {
						ocphealthcheckutil.SendEmailAlert(runningHost, fmt.Sprintf("/home/golanguser/.%s-%s.txt", "mcp", mcp.Name), spec, fmt.Sprintf("MachineConfig pool %s is degraded in cluster %s, please execute <oc get mcp %s and oc get nodes> to validate it", mcp.Name, runningHost, mcp.Name))
					}
				}
			} else if cond.Status == "False" {
				if slices.Contains(status.FailedChecks, fmt.Sprintf("mcp %s is degraded", mcp.Name)) {
					log.Log.Info(fmt.Sprintf("machineconfig pool %s is back to not degraded state", mcp.Name))
					idx := slices.Index(status.FailedChecks, fmt.Sprintf("mcp %s is degraded", mcp.Name))
					if len(status.FailedChecks) == 1 {
						status.FailedChecks = nil
					} else {
						status.FailedChecks = deleteOCPElementSlice(status.FailedChecks, idx)
					}
					if spec.SuspendEmailAlert != nil && !*spec.SuspendEmailAlert {
						ocphealthcheckutil.SendEmailAlert(runningHost, fmt.Sprintf("/home/golanguser/.%s-%s.txt", "mcp", mcp.Name), spec, fmt.Sprintf("MachineConfig pool %s is degraded in cluster %s, please execute <oc get mcp %s and oc get nodes> to validate it", mcp.Name, runningHost, mcp.Name))
					}
				}
			}
		}
	}
	return nil
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
	if sameNs, err := IsChildPolicyNamespace(clientset, newPo.Namespace); err != nil {
		log.Log.Info("unable to retrieve policy object namespace")
		return
	} else if sameNs {
		if !slices.Contains(status.FailedChecks, fmt.Sprintf("cgu update in progress for namespace %s", newPo.Namespace)) {
			log.Log.Info(fmt.Sprintf("Possible CGU update is in progress, ignore pod changes from namespace %s", newPo.Namespace))
			status.FailedChecks = append(status.FailedChecks, fmt.Sprintf("cgu update in progress for namespace %s", newPo.Namespace))
			if spec.SuspendEmailAlert != nil && !*spec.SuspendEmailAlert {
				ocphealthcheckutil.SendEmailAlert(runningHost, fmt.Sprintf("/home/golanguser/.%s-%s.txt", "cgu", newPo.Namespace), spec, fmt.Sprintf("possible CGU update is in progress for objects in namespace %s in cluster %s, no pod update alerts will be sent until CGU is compliant, please execute <oc get pods -n %s and oc get policy -A> to validate", newPo.Namespace, runningHost, newPo.Namespace))
			}
		}
		return
	} else {
		if slices.Contains(status.FailedChecks, fmt.Sprintf("cgu update in progress for namespace %s", newPo.Namespace)) {
			log.Log.Info(fmt.Sprintf("Appears that CGU update is completed for objects in namespace %s", newPo.Namespace))
			idx := slices.Index(status.FailedChecks, fmt.Sprintf("cgu update in progress for namespace %s", newPo.Namespace))
			if len(status.FailedChecks) == 1 {
				status.FailedChecks = nil
			} else {
				status.FailedChecks = deleteOCPElementSlice(status.FailedChecks, idx)
			}
			if spec.SuspendEmailAlert != nil && !*spec.SuspendEmailAlert {
				ocphealthcheckutil.SendEmailRecoveredAlert(runningHost, fmt.Sprintf("/home/golanguser/.%s-%s.txt", "cgu", newPo.Namespace), spec, fmt.Sprintf("Appears that CGU update is completed for objects in namespace %s in cluster %s, pod update alerts will continue to be sent", newPo.Namespace, runningHost))
			}
		}
	}
	for _, newCont := range newPo.Status.ContainerStatuses {
		if newCont.State.Terminated != nil && newCont.State.Terminated.ExitCode != 0 {
			if !slices.Contains(status.FailedChecks, fmt.Sprintf("pod %s container %s is terminated with non exit code 0 in namespace %s", newPo.Name, newCont.Name, newPo.Namespace)) {
				log.Log.Info(fmt.Sprintf("pod %s's container %s is terminated with non exit code 0 in namespace %s", newPo.Name, newCont.Name, newPo.Namespace))
				status.FailedChecks = append(status.FailedChecks, fmt.Sprintf("pod %s container %s is terminated with non exit code 0 in namespace %s", newPo.Name, newCont.Name, newPo.Namespace))
				if spec.SuspendEmailAlert != nil && !*spec.SuspendEmailAlert {
					ocphealthcheckutil.SendEmailAlert(runningHost, fmt.Sprintf("/home/golanguser/.%s-%s-%s.txt", newPo.Name, newCont.Name, newPo.Namespace), spec, fmt.Sprintf("pod %s's container %s is terminated with non exit code 0 in namespace %s in cluster %s", newPo.Name, newCont.Name, newPo.Namespace, runningHost))
				}
			}
		} else if newCont.State.Running != nil || (newCont.State.Terminated != nil && newCont.State.Terminated.ExitCode == 0) {
			// Assuming if pod has moved back to running from CrashLoopBackOff/others, the restart count will always be greater than 0
			if newCont.RestartCount > 0 {
				if newCont.LastTerminationState.Terminated != nil && newCont.LastTerminationState.Terminated.ExitCode != 0 && newCont.LastTerminationState.Terminated.FinishedAt.String() != "" {
					if podLastRestartTimerUp(newCont.LastTerminationState.Terminated.FinishedAt.String()) {
						if slices.Contains(status.FailedChecks, fmt.Sprintf("pod %s container %s is terminated with non exit code 0 in namespace %s", newPo.Name, newCont.Name, newPo.Namespace)) {
							log.Log.Info(fmt.Sprintf("pod %s's container %s is either running/completed in namespace %s", newPo.Name, newCont.Name, newPo.Namespace))
							idx := slices.Index(status.FailedChecks, fmt.Sprintf("pod %s container %s is terminated with non exit code 0 in namespace %s", newPo.Name, newCont.Name, newPo.Namespace))
							if len(status.FailedChecks) == 1 {
								status.FailedChecks = nil
							} else {
								status.FailedChecks = deleteOCPElementSlice(status.FailedChecks, idx)
							}
							if spec.SuspendEmailAlert != nil && !*spec.SuspendEmailAlert {
								ocphealthcheckutil.SendEmailRecoveredAlert(runningHost, fmt.Sprintf("/home/golanguser/.%s-%s.txt", newPo.Name, newCont.Name), spec, fmt.Sprintf("pod %s's container %s which was previously terminated with non exit code 0 is now either running/completed in namespace %s in cluster %s ", newPo.Name, newCont.Name, newPo.Namespace, runningHost))
							}
							os.Remove(fmt.Sprintf("/home/golanguser/.%s-%s-%s.txt", newPo.Name, newCont.Name, newPo.Namespace))
						}
					}
				}
			}
		}
		if newCont.State.Waiting != nil {
			if newCont.State.Waiting.Reason == "CrashLoopBackOff" {
				if !slices.Contains(status.FailedChecks, fmt.Sprintf("pod %s cont %s crashloopbackoff in namespace %s", newPo.Name, newCont.Name, newPo.Namespace)) {
					log.Log.Info(fmt.Sprintf("pod %s's container %s is in CrashLoopBackOff state in namespace %s", newPo.Name, newCont.Name, newPo.Namespace))
					status.FailedChecks = append(status.FailedChecks, fmt.Sprintf("pod %s cont %s crashloopbackoff in namespace %s", newPo.Name, newCont.Name, newPo.Namespace))
					if len(newPo.Spec.Volumes) > 0 {
						for _, vol := range newPo.Spec.Volumes {
							if vol.PersistentVolumeClaim != nil && vol.PersistentVolumeClaim.ClaimName != "" {
								// Get the persistent volume claim name
								pvc, err := clientset.CoreV1().PersistentVolumeClaims(newPo.Namespace).Get(context.Background(), vol.PersistentVolumeClaim.ClaimName, metav1.GetOptions{})
								if err != nil {
									if k8serrors.IsNotFound(err) {
										if spec.SuspendEmailAlert != nil && !*spec.SuspendEmailAlert {
											ocphealthcheckutil.SendEmailAlert(runningHost, fmt.Sprintf("/home/golanguser/.%s-%s-%s.txt", newPo, newCont.Name, newPo.Namespace), spec, fmt.Sprintf("Pod %s's container %s is in CrashLoopBackOff state, configured PVC %s doesn't exist in namespace %s in cluster %s, please execute <oc get pods %s -n %s -o json | jq .spec.volumes[] and oc get pvc %s -n %s -o json | jq .spec.volumeName> to validate it", newPo.Name, newCont.Name, pvc.Name, pvc.Namespace, runningHost, newPo.Name, newPo.Namespace, pvc.Name, pvc.Namespace))
										}
									} else {
										if spec.SuspendEmailAlert != nil && !*spec.SuspendEmailAlert {
											ocphealthcheckutil.SendEmailAlert(runningHost, fmt.Sprintf("/home/golanguser/.%s-%s-%s.txt", newPo, newCont.Name, newPo.Namespace), spec, fmt.Sprintf("Pod %s's container %s is in CrashLoopBackOff state, unable to retrieve configured PVC %s in namespace %s in cluster %s, please execute <oc get pods %s -n %s -o json | jq .spec.volumes[] and oc get pvc %s -n %s -o json | jq .spec.volumeName> to validate it", newPo.Name, newCont.Name, pvc.Name, pvc.Namespace, runningHost, newPo.Name, newPo.Namespace, pvc.Name, pvc.Namespace))
										}
									}
								}
								if affected, err := ocphealthcheckutil.PvHasDifferentNode(clientset, pvc.Spec.VolumeName, newPo.Spec.NodeName); err != nil {
									if spec.SuspendEmailAlert != nil && !*spec.SuspendEmailAlert {
										ocphealthcheckutil.SendEmailAlert(runningHost, fmt.Sprintf("/home/golanguser/.%s-%s-%s.txt", newPo, newCont.Name, newPo.Namespace), spec, fmt.Sprintf("Pod %s's container %s is in CrashLoopBackOff state, unable to retrieve volume attachment of volume %s in cluster %s, please execute <oc get pods %s -n %s -o json | jq .status.containerStatuses and oc get volumeattachments | grep %s > to validate it", newPo.Name, newCont.Name, pvc.Spec.VolumeName, runningHost, newPo, newPo.Namespace, pvc.Spec.VolumeName))
									}
								} else if err == nil && affected {
									if spec.SuspendEmailAlert != nil && !*spec.SuspendEmailAlert {
										ocphealthcheckutil.SendEmailAlert(runningHost, fmt.Sprintf("/home/golanguser/.%s-%s-%s.txt", newPo, newCont.Name, newPo.Namespace), spec, fmt.Sprintf("Pod %s's container %s is in CrashLoopBackOff state, volume attachment of volume %s is mounted on a different node in cluster %s, please execute <oc get pods %s -n %s -o json | jq .status.containerStatuses and oc get volumeattachments | grep %s > to validate it", newPo.Name, newCont.Name, pvc.Spec.VolumeName, runningHost, newPo, newPo.Namespace, pvc.Spec.VolumeName))
									}
								} else {
									// Check if it is due to image pull failure
									for _, cont := range newPo.Spec.Containers {
										if cont.Name == newCont.Name {
											if newCont.Image != cont.Image {
												if spec.SuspendEmailAlert != nil && !*spec.SuspendEmailAlert {
													ocphealthcheckutil.SendEmailAlert(runningHost, fmt.Sprintf("/home/golanguser/.%s-%s-%s.txt", newPo, newCont.Name, newPo.Namespace), spec, fmt.Sprintf("Pod %s's container %s is in CrashLoopBackOff state, volume attachment of volume %s is mounted on the SAME node, appears to be ErrImagePull error in cluster %s, please execute <oc get pods %s -n %s -o json | jq .status.containerStatuses and oc get volumeattachments | grep %s > to validate it", newPo.Name, newCont.Name, pvc.Spec.VolumeName, runningHost, newPo, newPo.Namespace, pvc.Spec.VolumeName))
												}
											}
										}
									}
								}
							} else {
								for _, cont := range newPo.Spec.Containers {
									if cont.Name == newCont.Name {
										if newCont.Image != cont.Image {
											if spec.SuspendEmailAlert != nil && !*spec.SuspendEmailAlert {
												ocphealthcheckutil.SendEmailAlert(runningHost, fmt.Sprintf("/home/golanguser/.%s-%s-%s.txt", newPo, newCont.Name, newPo.Namespace), spec, fmt.Sprintf("Pod %s's container %s is in CrashLoopBackOff state, appears to be ErrImagePull error in cluster %s, please execute <oc get pods %s -n %s -o json | jq .status.containerStatuses and oc  > to validate it", newPo.Name, newCont.Name, runningHost, newPo, newPo.Namespace))
											}
										} else {
											if spec.SuspendEmailAlert != nil && !*spec.SuspendEmailAlert {
												ocphealthcheckutil.SendEmailAlert(runningHost, fmt.Sprintf("/home/golanguser/.%s-%s-%s.txt", newPo.Name, newCont.Name, newPo.Namespace), spec, fmt.Sprintf("Pod %s's container %s is in CrashLoopBackOff state, no persistent volume is attached to the pod, doesn't seem to be ErrImagePull, could be other issues, in cluster %s, please execute <oc get pods %s -n %s -o json | jq .status.containerStatuses> to validate it", newPo.Name, newCont.Name, runningHost, newPo.Name, newPo.Namespace))
											}
										}
									}
								}
							}
						}
					}
				}
			}
		} else {
			// Assuming if pod has moved back to running from CrashLoopBackOff/others, the restart count will always be greater than 0
			if newCont.RestartCount > 0 {
				if newCont.LastTerminationState.Terminated != nil && newCont.LastTerminationState.Terminated.ExitCode != 0 && newCont.LastTerminationState.Terminated.FinishedAt.String() != "" {
					if podLastRestartTimerUp(newCont.LastTerminationState.Terminated.FinishedAt.String()) {
						if slices.Contains(status.FailedChecks, fmt.Sprintf("pod %s cont %s crashloopbackoff in namespace %s", newPo.Name, newCont.Name, newPo.Namespace)) {
							log.Log.Info(fmt.Sprintf("pod %s's container %s is either running/completed in namespace %s", newPo.Name, newCont.Name, newPo.Namespace))
							idx := slices.Index(status.FailedChecks, fmt.Sprintf("pod %s cont %s crashloopbackoff in namespace %s", newPo.Name, newCont.Name, newPo.Namespace))
							if len(status.FailedChecks) == 1 {
								status.FailedChecks = nil
							} else {
								status.FailedChecks = deleteOCPElementSlice(status.FailedChecks, idx)
							}
							if spec.SuspendEmailAlert != nil && !*spec.SuspendEmailAlert {
								ocphealthcheckutil.SendEmailRecoveredAlert(runningHost, fmt.Sprintf("/home/golanguser/.%s-%s-%s.txt", newPo.Name, newCont.Name, newPo.Namespace), spec, fmt.Sprintf("pod %s's container %s which was previously terminated with non exit code 0 is now either running/completed in namespace %s in cluster %s ", newPo.Name, newCont.Name, newPo.Namespace, runningHost))
							}
							os.Remove(fmt.Sprintf("/home/golanguser/.%s-%s-%s.txt", newPo.Name, newCont.Name, newPo.Namespace))
						}
					}
				}
			}
		}
	}
}

func CleanUpRunningPods(clientset *kubernetes.Clientset, spec *ocphealthcheckv1.OcpHealthCheckSpec, status *ocphealthcheckv1.OcpHealthCheckStatus, runningHost string) {
	if len(status.FailedChecks) > 0 {
		for _, stat := range status.FailedChecks {
			if strings.Contains(stat, "pod") {
				strs := strings.Split(stat, " ")
				if strings.Contains(stat, "terminated") {
					pod, err := clientset.CoreV1().Pods(strs[13]).Get(context.Background(), strs[1], metav1.GetOptions{})
					if err != nil {
						log.Log.Info(fmt.Sprintf("unable to retrieve pod %s", strs[1]))
					}
					for _, cont := range pod.Status.ContainerStatuses {
						if cont.State.Running != nil || (cont.State.Terminated != nil && cont.State.Terminated.ExitCode == 0) {
							if podLastRestartTimerUp(cont.LastTerminationState.Terminated.FinishedAt.String()) {
								if slices.Contains(status.FailedChecks, fmt.Sprintf("pod %s container %s is terminated with non exit code 0 in namespace %s", pod.Name, cont.Name, pod.Namespace)) {
									log.Log.Info(fmt.Sprintf("pod %s's container %s is either running/completed in namespace %s", pod.Name, cont.Name, pod.Namespace))
									idx := slices.Index(status.FailedChecks, fmt.Sprintf("pod %s container %s is terminated with non exit code 0 in namespace %s", pod.Name, cont.Name, pod.Namespace))
									if len(status.FailedChecks) == 1 {
										status.FailedChecks = nil
									} else {
										status.FailedChecks = deleteOCPElementSlice(status.FailedChecks, idx)
									}
									if spec.SuspendEmailAlert != nil && !*spec.SuspendEmailAlert {
										ocphealthcheckutil.SendEmailRecoveredAlert(runningHost, fmt.Sprintf("/home/golanguser/.%s-%s-%s.txt", pod.Name, cont.Name, pod.Namespace), spec, fmt.Sprintf("pod %s's container %s which was previously terminated with non exit code 0 is now either running/completed in namespace %s in cluster %s ", pod.Name, cont.Name, pod.Namespace, runningHost))
									}
									os.Remove(fmt.Sprintf("/home/golanguser/.%s-%s-%s.txt", pod.Name, cont.Name, pod.Namespace))
								}
							}
						}
					}
				} else {
					pod, err := clientset.CoreV1().Pods(strs[7]).Get(context.Background(), strs[1], metav1.GetOptions{})
					if err != nil {
						log.Log.Info(fmt.Sprintf("unable to retrieve pod %s", strs[1]))
					}
					for _, cont := range pod.Status.ContainerStatuses {
						if cont.State.Running != nil || (cont.State.Terminated != nil && cont.State.Terminated.ExitCode == 0) {
							if podLastRestartTimerUp(cont.LastTerminationState.Terminated.FinishedAt.String()) {
								if slices.Contains(status.FailedChecks, fmt.Sprintf("pod %s cont %s crashloopbackoff in namespace %s", pod.Name, cont.Name, pod.Namespace)) {
									log.Log.Info(fmt.Sprintf("pod %s's container %s is either running/completed in namespace %s", pod.Name, cont.Name, pod.Namespace))
									idx := slices.Index(status.FailedChecks, fmt.Sprintf("pod %s cont %s crashloopbackoff in namespace %s", pod.Name, cont.Name, pod.Namespace))
									if len(status.FailedChecks) == 1 {
										status.FailedChecks = nil
									} else {
										status.FailedChecks = deleteOCPElementSlice(status.FailedChecks, idx)
									}
									if spec.SuspendEmailAlert != nil && !*spec.SuspendEmailAlert {
										ocphealthcheckutil.SendEmailRecoveredAlert(runningHost, fmt.Sprintf("/home/golanguser/.%s-%s-%s.txt", pod.Name, cont.Name, pod.Namespace), spec, fmt.Sprintf("pod %s's container %s which was previously terminated with non exit code 0 is now either running/completed in namespace %s in cluster %s ", pod.Name, cont.Name, pod.Namespace, runningHost))
									}
									os.Remove(fmt.Sprintf("/home/golanguser/.%s-%s-%s.txt", pod.Name, cont.Name, pod.Namespace))
								}
							}
						}
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
	for anno, val := range node.Annotations {
		// to be updated
		if anno == "machineconfiguration.openshift.io/state" {
			if val != MACHINECONFIGUPDATEDONE {
				ocphealthcheckutil.EnableMCPAnno(mcp, node.Name, val)
			} else {
				ocphealthcheckutil.DisableMCPAnno(mcp)
			}
		}
	}
	if node.Spec.Unschedulable {
		if !slices.Contains(status.FailedChecks, fmt.Sprintf("node %s has become unschedulable", node.Name)) {
			ocphealthcheckutil.EnableMCP(mcp, node.Name, "Unschedulable")
			log.Log.Info(fmt.Sprintf("node %s has become unschedulable", node.Name))
			status.FailedChecks = append(status.FailedChecks, fmt.Sprintf("node %s has become unschedulable", node.Name))
			if spec.SuspendEmailAlert != nil && !*spec.SuspendEmailAlert {
				ocphealthcheckutil.SendEmailAlert(runningHost, fmt.Sprintf("/home/golanguser/.%s-%s.txt", node.Name, "sched"), spec, fmt.Sprintf("node %s has become unschedulable in cluster %s, please execute <oc get nodes %s > to validate it", node.Name, runningHost, node.Name))
			}
		}
	} else {
		if slices.Contains(status.FailedChecks, fmt.Sprintf("node %s has become unschedulable", node.Name)) {
			ocphealthcheckutil.DisableMCP(mcp)
			log.Log.Info(fmt.Sprintf("node %s has become schedulable again", node.Name))
			idx := slices.Index(status.FailedChecks, fmt.Sprintf("node %s has become unschedulable", node.Name))
			if len(status.FailedChecks) == 1 {
				status.FailedChecks = nil
			} else {
				status.FailedChecks = deleteOCPElementSlice(status.FailedChecks, idx)
			}
			if spec.SuspendEmailAlert != nil && !*spec.SuspendEmailAlert {
				ocphealthcheckutil.SendEmailRecoveredAlert(runningHost, fmt.Sprintf("/home/golanguser/.%s-%s.txt", node.Name, "sched"), spec, fmt.Sprintf("node %s which was previously unschedulable is now schedulable again in cluster %s", node.Name, runningHost))
			}
			os.Remove(fmt.Sprintf("/home/golanguser/.%s-%s.txt", node.Name, "sched"))
		}
	}
	for _, cond := range node.Status.Conditions {
		if cond.Type == "Ready" && cond.Status == "False" {
			if !slices.Contains(status.FailedChecks, fmt.Sprintf("node %s has become NotReady", node.Name)) {
				ocphealthcheckutil.EnableMCP(mcp, node.Name, "NotReady")
				log.Log.Info(fmt.Sprintf("node %s has become NotReady", node.Name))
				status.FailedChecks = append(status.FailedChecks, fmt.Sprintf("node %s has become NotReady", node.Name))
				if spec.SuspendEmailAlert != nil && !*spec.SuspendEmailAlert {
					ocphealthcheckutil.SendEmailAlert(runningHost, fmt.Sprintf("/home/golanguser/.%s-%s.txt", node.Name, "ready"), spec, fmt.Sprintf("node %s has become NotReady in cluster %s, please execute <oc get nodes %s > to validate it", node.Name, runningHost, node.Name))
				}
			}
		} else if cond.Type == "Ready" && cond.Status == "True" {
			if slices.Contains(status.FailedChecks, fmt.Sprintf("node %s has become NotReady", node.Name)) {
				ocphealthcheckutil.DisableMCP(mcp)
				log.Log.Info(fmt.Sprintf("node %s has become Ready again", node.Name))
				idx := slices.Index(status.FailedChecks, fmt.Sprintf("node %s has become NotReady", node.Name))
				if len(status.FailedChecks) == 1 {
					status.FailedChecks = nil
				} else {
					status.FailedChecks = deleteOCPElementSlice(status.FailedChecks, idx)
				}
				if spec.SuspendEmailAlert != nil && !*spec.SuspendEmailAlert {
					ocphealthcheckutil.SendEmailRecoveredAlert(runningHost, fmt.Sprintf("/home/golanguser/.%s-%s.txt", node.Name, "ready"), spec, fmt.Sprintf("node %s which was previously marked as NotReady is now Ready again in cluster %s, please execute <oc get nodes %s > to validate it", node.Name, runningHost, node.Name))
				}
				os.Remove(fmt.Sprintf("/home/golanguser/.%s-%s.txt", node.Name, "ready"))
			}
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
	if !IsChildPolicy(policy) {
		if policy.Status.ComplianceState == "NonCompliant" || policy.Spec.Disabled {
			if !slices.Contains(status.FailedChecks, fmt.Sprintf("policy %s has become non-compliant in namespace %s", policy.Name, policy.Namespace)) {
				log.Log.Info(fmt.Sprintf("policy %s has become non-compliant in namespace %s", policy.Name, policy.Namespace))
				status.FailedChecks = append(status.FailedChecks, fmt.Sprintf("policy %s has become non-compliant in namespace %s", policy.Name, policy.Namespace))
				if spec.SuspendEmailAlert != nil && !*spec.SuspendEmailAlert {
					ocphealthcheckutil.SendEmailAlert(runningHost, fmt.Sprintf("/home/golanguser/.%s-%s.txt", policy.Name, "noncomplaint"), spec, fmt.Sprintf("policy %s is either non-compliant/disabled in namespace %s in cluster %s, please execute <oc get policy %s -n %s -o json | jq .status> to validate it", policy.Name, policy.Namespace, runningHost, policy.Name, policy.Namespace))
				}
			}
		} else {
			if slices.Contains(status.FailedChecks, fmt.Sprintf("policy %s has become non-compliant in namespace %s", policy.Name, policy.Namespace)) {
				log.Log.Info(fmt.Sprintf("policy %s has become compliant again in namespace %s", policy.Name, policy.Namespace))
				idx := slices.Index(status.FailedChecks, fmt.Sprintf("policy %s has become non-compliant in namespace %s", policy.Name, policy.Namespace))
				if len(status.FailedChecks) == 1 {
					status.FailedChecks = nil
				} else {
					status.FailedChecks = deleteOCPElementSlice(status.FailedChecks, idx)
				}
				if spec.SuspendEmailAlert != nil && !*spec.SuspendEmailAlert {
					ocphealthcheckutil.SendEmailRecoveredAlert(runningHost, fmt.Sprintf("/home/golanguser/.%s-%s.txt", policy.Name, "noncomplaint"), spec, fmt.Sprintf("policy %s which was previously non-compliant/disabled is now compliant/enabled again in namespace %s in cluster %s", policy.Name, policy.Namespace, runningHost))
				}
				os.Remove(fmt.Sprintf("policy %s has become non-compliant in namespace %s", policy.Name, policy.Namespace))
			}
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
	if slices.Contains(status.FailedChecks, fmt.Sprintf("policy %s has become non-compliant in namespace %s", policy.Name, policy.Namespace)) {
		log.Log.Info(fmt.Sprintf("policy %s has become compliant again in namespace %s", policy.Name, policy.Namespace))
		idx := slices.Index(status.FailedChecks, fmt.Sprintf("policy %s has become non-compliant in namespace %s", policy.Name, policy.Namespace))
		if len(status.FailedChecks) == 1 {
			status.FailedChecks = nil
		} else {
			status.FailedChecks = deleteOCPElementSlice(status.FailedChecks, idx)
		}
		if spec.SuspendEmailAlert != nil && !*spec.SuspendEmailAlert {
			ocphealthcheckutil.SendEmailRecoveredAlert(runningHost, fmt.Sprintf("/home/golanguser/.%s-%s.txt", policy.Name, "noncomplaint"), spec, fmt.Sprintf("policy %s which was previously non-compliant/disabled is now deleted in namespace %s in cluster %s", policy.Name, policy.Namespace, runningHost))
		}
		os.Remove(fmt.Sprintf("policy %s has become non-compliant in namespace %s", policy.Name, policy.Namespace))
	}
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
	if len(status.FailedChecks) > 0 {
		for _, stat := range status.FailedChecks {
			for _, newCont := range po.Status.ContainerStatuses {
				if strings.Contains(stat, fmt.Sprintf("pod %s container %s is terminated with non exit code 0 in namespace %s", po.Name, newCont.Name, po.Namespace)) {
					idx := slices.Index(status.FailedChecks, fmt.Sprintf("pod %s container %s is terminated with non exit code 0 in namespace %s", po.Name, newCont.Name, po.Namespace))
					if len(status.FailedChecks) == 1 {
						status.FailedChecks = nil
					} else {
						status.FailedChecks = deleteOCPElementSlice(status.FailedChecks, idx)
					}
				} else if strings.Contains(stat, fmt.Sprintf("pod %s cont %s crashloopbackoff in namespace %s", po.Name, newCont.Name, po.Namespace)) {
					idx := slices.Index(status.FailedChecks, fmt.Sprintf("pod %s cont %s crashloopbackoff in namespace %s", po.Name, newCont.Name, po.Namespace))
					if len(status.FailedChecks) == 1 {
						status.FailedChecks = nil
					} else {
						status.FailedChecks = deleteOCPElementSlice(status.FailedChecks, idx)
					}
				}
			}
		}
	}
	log.Log.Info(fmt.Sprintf("pod %s has been deleted from namespace %s", po.Name, po.Namespace))
}
