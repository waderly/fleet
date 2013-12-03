package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"path"
	"time"

	"github.com/coreos/go-etcd/etcd"

	"github.com/coreos/coreinit/job"
	"github.com/coreos/coreinit/machine"
)

const (
	keyPrefix      = "/coreos.com/coreinit/"
	lockPrefix     = "/locks/"
	machinePrefix  = "/machines/"
	requestPrefix  = "/request/"
	schedulePrefix = "/schedule/"
	jobWatchPrefix = "/watch/"
	statePrefix    = "/state/"
)

type Registry struct {
	etcd *etcd.Client
}

func New() (registry *Registry) {
	etcdC := etcd.NewClient(nil)
	etcdC.SetConsistency(etcd.WEAK_CONSISTENCY)
	return &Registry{etcdC}
}

// Describe all active Machines
func (r *Registry) GetActiveMachines() []machine.Machine {
	key := path.Join(keyPrefix, machinePrefix)
	resp, err := r.etcd.Get(key, false, true)

	// Assume the error was KeyNotFound and return an empty data structure
	if err != nil {
		return make([]machine.Machine, 0)
	}

	machines := make([]machine.Machine, 0)
	for _, kv := range resp.Kvs {
		_, bootId := path.Split(kv.Key)
		machine := machine.New(bootId)

		// This is a hacky way of telling if a Machine is reporting state
		addrs := r.getMachineAddrs(machine)
		if len(addrs) > 0 {
			machines = append(machines, *machine)
		}
	}

	return machines
}

func (r *Registry) getMachineAddrs(m *machine.Machine) []machine.IPAddress {
	key := path.Join(keyPrefix, machinePrefix, m.BootId, "addrs")
	resp, err := r.etcd.Get(key, false, true)

	addrs := make([]machine.IPAddress, 0)

	// Assume this is KeyNotFound and return an empty data structure
	if err != nil {
		return addrs
	}

	//TODO: Handle the error generated by unmarshal
	unmarshal(resp.Value, &addrs)

	return addrs
}

// Push Machine Addr data to coreinit
func (r *Registry) SetMachineAddrs(machine *machine.Machine, addrs []machine.IPAddress, ttl time.Duration) {
	//TODO: Handle the error generated by marshal
	json, _ := marshal(addrs)
	key := path.Join(keyPrefix, machinePrefix, machine.BootId, "addrs")
	r.etcd.Set(key, json, uint64(ttl.Seconds()))
}

// Submit a new JobRequest to coreinit
func (r *Registry) AddRequest(req *job.JobRequest) {
	key := path.Join(keyPrefix, requestPrefix, req.ID.String())
	//TODO: Handle the error generated by marshal
	json, _ := marshal(req)
	r.etcd.Set(key, json, 0)
}

// Remove a given JobRequest from coreinit
func (r *Registry) ResolveRequest(req *job.JobRequest) {
	key := path.Join(keyPrefix, requestPrefix, req.ID.String())
	r.etcd.Delete(key, true)
}

// List the jobs a given Machine is scheduled to run
func (r *Registry) GetMachineJobs(machine *machine.Machine) map[string]job.Job {
	key := path.Join(keyPrefix, machinePrefix, machine.BootId, schedulePrefix)
	resp, err := r.etcd.Get(key, false, true)

	// Assume the error was KeyNotFound and return an empty data structure
	if err != nil {
		return make(map[string]job.Job, 0)
	}

	jobs := make(map[string]job.Job, len(resp.Kvs))
	for _, kv := range resp.Kvs {
		name := path.Base(kv.Key)

		var payload job.JobPayload
		err := unmarshal(kv.Value, &payload)

		if err == nil {
			j, _ := job.NewJob(name, nil, &payload)
			//FIXME: This will hide duplicate jobs!
			jobs[j.Name] = *j
		} else {
			log.Print(err)
		}
	}
	return jobs
}

// List the jobs all Machines are scheduled to run
func (r *Registry) GetScheduledJobs() map[string]job.Job {
	machines := r.GetActiveMachines()
	jobs := map[string]job.Job{}
	for _, mach := range machines {
		for name, j := range r.GetMachineJobs(&mach) {
			//FIXME: This will hide duplicate jobs!
			jobs[name] = j
		}
	}
	return jobs
}

func (r *Registry) GetJobPayload(j *job.Job) *job.JobPayload {
	key := path.Join(keyPrefix, schedulePrefix, j.Name)
	resp, err := r.etcd.Get(key, false, true)

	// Assume the error was KeyNotFound and return an empty data structure
	if err != nil {
		return nil
	}

	var payload job.JobPayload
	//TODO: Handle the error generated by unmarshal
	unmarshal(resp.Value, &payload)
	return &payload
}

// Get the current JobState of the provided Job
func (r *Registry) GetJobState(j *job.Job) *job.JobState {
	key := path.Join(keyPrefix, statePrefix, j.Name)
	resp, err := r.etcd.Get(key, false, true)

	// Assume the error was KeyNotFound and return an empty data structure
	if err != nil {
		return nil
	}

	var state job.JobState
	//TODO: Handle the error generated by unmarshal
	unmarshal(resp.Value, &state)
	return &state
}

// Add a new JobWatch to coreinit
func (r *Registry) SaveJobWatch(watch *job.JobWatch) {
	key := path.Join(keyPrefix, jobWatchPrefix, watch.Payload.Name)
	//TODO: Handle the error generated by marshal
	json, _ := marshal(watch)
	r.etcd.Set(key, json, 0)
}

// Schedule a Job to a given Machine
func (r *Registry) ScheduleMachineJob(job *job.Job, machine *machine.Machine) {
	key := path.Join(keyPrefix, machinePrefix, machine.BootId, schedulePrefix, job.Name)
	//TODO: Handle the error generated by marshal
	json, _ := marshal(job.Payload)
	log.Printf("Registry: setting key %s to value %s", key, json)
	r.etcd.Set(key, json, 0)
}

// StopJob removes the Job from any Machine's schedule. It also removes any
// relevant JobWatch objects.
func (r *Registry) StopJob(job *job.Job) {
	key := path.Join(keyPrefix, jobWatchPrefix, job.Name)
	r.etcd.Delete(key, true)

	for _, m := range r.GetActiveMachines() {
		name := fmt.Sprintf("%s.%s", m.BootId, job.Name)
		key := path.Join(keyPrefix, machinePrefix, m.BootId, schedulePrefix, name)
		r.etcd.Delete(key, true)
	}
}

// Persist the changes in a provided Machine's Job to etcd with the provided TTL
func (r *Registry) UpdateJob(job *job.Job, ttl time.Duration) {
	key := path.Join(keyPrefix, statePrefix, job.Name)
	//TODO: Handle the error generated by marshal
	json, _ := marshal(job.State)
	r.etcd.Set(key, json, uint64(ttl.Seconds()))
}

// Attempt to acquire a lock in etcd on an arbitrary string. Returns true if
// successful, otherwise false.
func (r *Registry) AcquireLock(name string, context string, ttl time.Duration) bool {
	key := path.Join(keyPrefix, lockPrefix, name)
	_, err := r.etcd.Create(key, context, uint64(ttl.Seconds()))
	return err == nil
}

func marshal(obj interface{}) (string, error) {
	encoded, err := json.Marshal(obj)
	if err == nil {
		return string(encoded), nil
	} else {
		return "", errors.New(fmt.Sprintf("Unable to JSON-serialize object: %s", err))
	}
}

func unmarshal(val string, obj interface{}) error {
	err := json.Unmarshal([]byte(val), &obj)
	if err == nil {
		return nil
	} else {
		return errors.New(fmt.Sprintf("Unable to JSON-deserialize object: %s", err))
	}
}
