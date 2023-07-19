package kubelocker

import (
	"context"
	"fmt"
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
	namespace   string
	clientID    string
	leaseClient coordinationclientv1.LeaseInterface
	cfg         kubelockerCfg

	_workLog []string
}

type kubelockerCfg struct {
	name      string
	leaseTtl  time.Duration
	maxWait   time.Duration
	retryWait time.Duration
}

// NewLocker creates a Locker
func Newkubelocker(kubeClientset *kubernetes.Clientset, namespace string, cfgs ...kubelockerCfg) (*kubelocker, error) {

	cfg := kubelockerCfg{
		name:      "kubelocker",
		leaseTtl:  55 * time.Second,
		maxWait:   120 * time.Second,
		retryWait: 6 * time.Second,
	}

	if len(cfgs) == 1 {
		cfg = cfgs[0]
	}

	// create the Lease if it doesn't exist
	leaseClient := kubeClientset.CoordinationV1().Leases(namespace)
	_, err := leaseClient.Get(context.TODO(), cfg.name, metav1.GetOptions{})
	if err != nil {
		if !k8errors.IsNotFound(err) {
			return nil, err
		}
		lease := &coordinationv1.Lease{
			ObjectMeta: metav1.ObjectMeta{
				Name: cfg.name,
			},
			Spec: coordinationv1.LeaseSpec{
				LeaseTransitions: pointer.Int32(0),
			},
		}
		_, err := leaseClient.Create(context.TODO(), lease, metav1.CreateOptions{})
		if err != nil {
			return nil, err
		}
	}

	return &kubelocker{
		clientset:   kubeClientset,
		namespace:   namespace,
		clientID:    uuid.New().String(),
		leaseClient: leaseClient,
		cfg:         cfg,
		_workLog:    []string{},
	}, nil
}

// Lock will block until the client is the holder of the Lease resource
func (l *kubelocker) Lock() error {
	ttl := l.cfg.maxWait

	// block until we get a lock
	for {
		if ttl < 0 {
			return fmt.Errorf("timeout while trying to get a lease for lock: %+v", l)
		}
		// get the Lease
		lease, err := l.leaseClient.Get(context.TODO(), l.cfg.name, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("could not get Lease resource for lock: %v", err)
		}

		if lease.Spec.HolderIdentity != nil {
			if lease.Spec.LeaseDurationSeconds == nil {
				l._workLog = append(l._workLog, fmt.Sprintf("%v, %v is waiting for %v (no expiry), ttl: %v", time.Now(), l.Id(), *lease.Spec.HolderIdentity, ttl))
				time.Sleep(l.cfg.retryWait)
				ttl -= l.cfg.retryWait
				continue
			}

			acquireTime := lease.Spec.AcquireTime.Time
			leaseDuration := time.Duration(*lease.Spec.LeaseDurationSeconds) * time.Second
			exp := acquireTime.Add(leaseDuration)
			if exp.After(time.Now()) {
				l._workLog = append(l._workLog, fmt.Sprintf("%v, %v is waiting for %v (exp in: %v), ttl: %v", time.Now(), l.Id(), lease.Spec.HolderIdentity, time.Until(exp), ttl))
				time.Sleep(l.cfg.retryWait)
				ttl -= l.cfg.retryWait
				continue
			}
		}

		// try to lock
		lease.Spec.HolderIdentity = pointer.String(l.clientID)
		if lease.Spec.LeaseTransitions != nil {
			lease.Spec.LeaseTransitions = pointer.Int32((*lease.Spec.LeaseTransitions) + 1)
		} else {
			lease.Spec.LeaseTransitions = pointer.Int32((*lease.Spec.LeaseTransitions) + 1)
		}
		lease.Spec.AcquireTime = &metav1.MicroTime{time.Now()}
		if l.cfg.leaseTtl.Seconds() > 0 {
			lease.Spec.LeaseDurationSeconds = pointer.Int32(int32(l.cfg.leaseTtl.Seconds()))
		}
		_, err = l.leaseClient.Update(context.TODO(), lease, metav1.UpdateOptions{})
		if err == nil { // locked
			l._workLog = append(l._workLog, fmt.Sprintf("%v, %v acquired lock in %v", time.Now(), l.Id(), l.cfg.maxWait-ttl))
			break
		}

		if !k8errors.IsConflict(err) { // unexpected
			return fmt.Errorf("lock: error when trying to update Lease: %v", err)
		}

		// another client beat us to the lock
		l._workLog = append(l._workLog, fmt.Sprintf("beaten by another client, will retry, ttl: %v", ttl))
		time.Sleep(l.cfg.retryWait)
		ttl -= l.cfg.retryWait
	}
	return nil
}

// Unlock will remove the client as the holder of the Lease resource
func (l *kubelocker) Unlock() error {

	lease, err := l.leaseClient.Get(context.TODO(), l.cfg.name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("[ERROR] could not get Lease resource for lock: %v", err)

	}

	// the holder has to have a value and has to be our ID for us to be able to unlock
	if lease.Spec.HolderIdentity == nil {
		return fmt.Errorf("[ERROR] unlock: no lock holder value")

	}

	if *lease.Spec.HolderIdentity != l.clientID {
		return fmt.Errorf("[ERROR] unlock: not the lock holder")
	}

	lease.Spec.HolderIdentity = nil
	lease.Spec.AcquireTime = nil
	lease.Spec.LeaseDurationSeconds = nil
	_, err = l.leaseClient.Update(context.TODO(), lease, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("[ERROR] unlock: error when trying to update Lease: %v", err)
	}
	return nil
}

func (l *kubelocker) WorkLog() []string {
	return l._workLog
}

func (l *kubelocker) Id() string {
	return fmt.Sprintf("<%v,%v/%v>", l.clientID, l.namespace, l.cfg.name)
}
