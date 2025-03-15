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
	"sync"
	"time"

	ocphealthcheckv1 "github.com/barani129/ocphealthcheckinf/api/v1"
	ocphealthcheckutil "github.com/barani129/ocphealthcheckinf/internal/util"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
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
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;update;patch;delete

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
	_, status, err := ocphealthcheckutil.GetSpecAndStatus(ocpScan)
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
	b := false
	var mcpParam = new(ocphealthcheckutil.MCPStruct)
	mcpParam.IsMCPInProgress = &b
	log.Log.Info("Starting dynamic informer factory")
	nsFactory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(clientset, time.Minute, corev1.NamespaceAll, nil)
	log.Log.Info("Starting pod informer")
	podInformer := nsFactory.ForResource(podResource).Informer()
	log.Log.Info("Starting node informer")
	nodeInformer := nsFactory.ForResource(nodeResource).Informer()
	mcpInformer := nsFactory.ForResource(mcpResource).Informer()
	mux := &sync.RWMutex{}
	synced := false

	// logic for mcp handling: check if mcp is in progress, if in progress, fetch the node based on labels
	// mcp.spec.machineConfigSelector.matchLabels
	// check if annotation["machineconfiguration.openshift.io/state"] is set to other than Done
	// if not, assuming that mcp is actually in progress and exiting, otherwise continue with the flow
	mcpInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(oldObj, newObj interface{}) {
			mux.RLock()
			defer mux.RUnlock()
			if !synced {
				return
			}
			err := onMCPUpdate(newObj, staticClientSet, mcpParam)
			if err != nil {
				return
			}
			log.Log.Info("MachineConfigUpdate is not progress, proceeding further")
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
				ocphealthcheckutil.OnPodUpdate(oldObj)
			}
		},
	})
	log.Log.Info("Running pod informer")
	nodeInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(oldObj, newObj interface{}) {
			mux.RLock()
			defer mux.RUnlock()
			if !synced {
				return
			}
			if mcpParam.IsMCPInProgress != nil && !*mcpParam.IsMCPInProgress {
				ocphealthcheckutil.OnNodeUpdate(newObj)
			}
		},
	})
	// go podInformer.Run(context.Background().Done())
	nsFactory.Start(context.Background().Done())
	log.Log.Info("Waiting for cache sync")
	isSynced := cache.WaitForCacheSync(context.Background().Done(), podInformer.HasSynced, nodeInformer.HasSynced, mcpInformer.HasSynced)
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

func onMCPUpdate(newObj interface{}, staticClientSet *kubernetes.Clientset, mcpParam *ocphealthcheckutil.MCPStruct) error {
	mcp := new(mcfgv1.MachineConfigPool)
	err := ocphealthcheckutil.ConvertUnStructureToStructured(newObj, mcp)
	if err != nil {
		log.Log.Error(err, "failed to convert")
	}
	for _, cond := range mcp.Status.Conditions {
		if cond.Type == "Updating" {
			if cond.Status == "True" {
				// Check node annotations to validate it
				if mcp.Spec.MachineConfigSelector.MatchLabels != nil {
					actualUpdate, nodeAnno, err := ocphealthcheckutil.CheckNodeMcpAnnotations(staticClientSet, mcp.Spec.MachineConfigSelector.MatchLabels)
					if err != nil {
						log.Log.Error(err, "unable to check node MCP annotations")
						return err
					}
					if actualUpdate {
						a := true
						mcpParam.IsMCPInProgress = &a
						if len(nodeAnno) > 0 {
							mcpParam.MCPNode = nodeAnno
							for nodeA, state := range mcpParam.MCPNode {
								log.Log.Info(fmt.Sprintf("MachineConfig pool %s update is in progress and node %s's annotation has been set to state %s, please check ", mcp.Name, nodeA, state))
							}
						}
						return fmt.Errorf("actual mcp update is in progress")
					} else {
						nodeAffected, nodeStatus, err := ocphealthcheckutil.CheckNodeReadiness(staticClientSet, mcp.Spec.MachineConfigSelector.MatchLabels)
						if err != nil {
							log.Log.Error(err, "unable to check node status")
							return err
						}
						if nodeAffected {
							if len(nodeStatus) > 0 {
								for nodeS, status := range nodeStatus {
									log.Log.Info(fmt.Sprintf("MachineConfig pool %s update has been set to true, node %s has become %s, possible manual action", mcp.Name, nodeS, status))
								}
							}
						} else {
							log.Log.Info("machineconfig pool %s update is in progress, but all nodes are found ready and schedulable, please check")
						}
					}
				}
			}
		}
	}
	return nil
}
