package kubelocker

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/pointer"

	"k8s.io/client-go/kubernetes"

	coordinationv1 "k8s.io/api/coordination/v1"
	k8errors "k8s.io/apimachinery/pkg/api/errors"
	coordinationclientv1 "k8s.io/client-go/kubernetes/typed/coordination/v1"
)

// ########################## kubelocker ##########################

type kubelocker struct {
	clientset   *kubernetes.Clientset
	leaseClient coordinationclientv1.LeaseInterface
	namespace   string
	name        string
	clientID    string
	retryWait   time.Duration
	maxWait     time.Duration
	ttl         time.Duration
}

// NewLocker creates a Locker
func Newkubelocker(kubeClientset *kubernetes.Clientset, namespace string) *kubelocker {
	name := "kubelocker"

	// create the Lease if it doesn't exist
	leaseClient := kubeClientset.CoordinationV1().Leases(namespace)
	_, err := leaseClient.Get(context.TODO(), name, metav1.GetOptions{})
	if err != nil {
		if !k8errors.IsNotFound(err) {
			panic("failed to create lease: " + err.Error())
		}
		lease := &coordinationv1.Lease{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
			},
			Spec: coordinationv1.LeaseSpec{
				LeaseTransitions: pointer.Int32(0),
			},
		}
		_, err := leaseClient.Create(context.TODO(), lease, metav1.CreateOptions{})
		if err != nil {
			panic("failed to create lease: " + err.Error())
		}
	}
	return &kubelocker{
		name:        name,
		namespace:   namespace,
		clientID:    uuid.New().String(),
		retryWait:   1000 * time.Millisecond,
		maxWait:     30 * time.Second,
		leaseClient: leaseClient,
		clientset:   kubeClientset,
	}
}

// Lock will block until the client is the holder of the Lease resource
func (l *kubelocker) Lock() {
	ttl := l.maxWait

	// block until we get a lock
	for {
		if ttl < 0 {
			panic(fmt.Sprintf("timeout while trying to get a lease for lock: %v", l))
		}
		// get the Lease
		lease, err := l.leaseClient.Get(context.TODO(), l.name, metav1.GetOptions{})
		if err != nil {
			panic(fmt.Sprintf("could not get Lease resource for lock: %v", err))
		}

		if lease.Spec.HolderIdentity != nil {
			if lease.Spec.LeaseDurationSeconds == nil {
				// The lock is already held and has no expiry
				time.Sleep(l.retryWait)
				ttl -= l.retryWait
				continue
			}

			acquireTime := lease.Spec.AcquireTime.Time
			leaseDuration := time.Duration(*lease.Spec.LeaseDurationSeconds) * time.Second

			if acquireTime.Add(leaseDuration).After(time.Now()) {
				// The lock is already held and hasn't expired yet
				time.Sleep(l.retryWait)
				ttl -= l.retryWait
				continue
			}
		}

		// nobody holds the lock, try and lock it
		lease.Spec.HolderIdentity = pointer.String(l.clientID)
		if lease.Spec.LeaseTransitions != nil {
			lease.Spec.LeaseTransitions = pointer.Int32((*lease.Spec.LeaseTransitions) + 1)
		} else {
			lease.Spec.LeaseTransitions = pointer.Int32((*lease.Spec.LeaseTransitions) + 1)
		}
		lease.Spec.AcquireTime = &metav1.MicroTime{time.Now()}
		if l.ttl.Seconds() > 0 {
			lease.Spec.LeaseDurationSeconds = pointer.Int32(int32(l.ttl.Seconds()))
		}
		_, err = l.leaseClient.Update(context.TODO(), lease, metav1.UpdateOptions{})
		if err == nil {
			// we got the lock, break the loop
			break
		}

		if !k8errors.IsConflict(err) {
			// if the error isn't a conflict then something went horribly wrong
			panic(fmt.Sprintf("lock: error when trying to update Lease: %v", err))
		}

		// Another client beat us to the lock
		time.Sleep(l.retryWait)
		ttl -= l.retryWait
	}
}

// Unlock will remove the client as the holder of the Lease resource
func (l *kubelocker) Unlock() {

	lease, err := l.leaseClient.Get(context.TODO(), l.name, metav1.GetOptions{})
	if err != nil {
		panic(fmt.Sprintf("could not get Lease resource for lock: %v", err))
	}

	// the holder has to have a value and has to be our ID for us to be able to unlock
	if lease.Spec.HolderIdentity == nil {
		panic("unlock: no lock holder value")
	}

	if *lease.Spec.HolderIdentity != l.clientID {
		panic("unlock: not the lock holder")
	}

	lease.Spec.HolderIdentity = nil
	lease.Spec.AcquireTime = nil
	lease.Spec.LeaseDurationSeconds = nil
	_, err = l.leaseClient.Update(context.TODO(), lease, metav1.UpdateOptions{})
	if err != nil {
		panic(fmt.Sprintf("unlock: error when trying to update Lease: %v", err))
	}
}

func (l *kubelocker) Util_watiForDeployments(nsName string, timeout time.Duration) error {
	ttl := timeout
	wait := 5 * time.Second
	for ttl > 0 {
		time.Sleep(wait)
		ttl -= wait
		log.Printf("ttl: %v (%v)", ttl, nsName)
		ds, err := l.clientset.AppsV1().Deployments(nsName).List(context.Background(), metav1.ListOptions{})
		if err != nil {
			return err
		}
		done := true
		for _, d := range ds.Items {
			if d.Status.Replicas != d.Status.AvailableReplicas ||
				d.Status.Replicas != d.Status.ReadyReplicas ||
				d.Status.Replicas != d.Status.UpdatedReplicas {
				done = false
				log.Printf("waiting for: %v (%v)", d.Name, nsName)
			}
		}
		if done {
			log.Printf("waited: %v", timeout-ttl)
			return nil
		}
	}
	return errors.New("timeout")
}
