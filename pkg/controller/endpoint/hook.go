package endpoint

import (
	"flag"
	"fmt"
	"net/http"
	"strconv"

	"github.com/chinglinwen/log"
	"github.com/natefinch/lumberjack"
	resty "gopkg.in/resty.v1"
	"k8s.io/kubernetes/pkg/api/v1"
)

var (
	//upstreamnChangeAPI = flag.String("upstreamc", "http://upstream-pre.sched.qianbao-inc.com/up_nginx_state/", "upstream change api url")
	hookAPI = flag.String("hook-api", "http://localhost:8081/hook", "hook api url")
	logPath = flag.String("hook-logpath", "/var/log/hook.log", "hook log path")
)

func init() {
	log.SetOutput(&lumberjack.Logger{
		Filename:   "/var/log/hook.log",
		MaxSize:    500, // megabytes
		MaxBackups: 3,
		MaxAge:     28, //days
	})
}

type Phase int

const (
	PhaseUnknown = iota
	PhaseADD
	PhaseUPDATE
	PhaseDEL
)

func (p Phase) String() string {
	switch p {
	case PhaseADD:
		return "ADD"
	case PhaseDEL:
		return "DEL"
	case PhaseUPDATE:
		return "UPDATE"
	default:
		return "UNKNOWN"
	}
}

type PodInfo struct {
	Name      string
	Namespace string
	Phase     Phase
	IP        string
	Port      string
	Reason    string
	Msg       string
}

func parsePod(phase Phase, pod *v1.Pod) (p PodInfo, err error) {
	if len(pod.Spec.Containers) != 1 && phase != PhaseDEL {
		err = fmt.Errorf("got many container in a pod, skip")
		return
	}

	pod0 := pod.Spec.Containers[0]
	if len(pod0.Ports) == 0 && phase != PhaseDEL {
		err = fmt.Errorf("no any port in container, skip")
		return
	}

	ip, reason, msg := convertPod(pod.Status)

	// name := pod0.Name //skip this use pod name only, so no multiple container in a pod
	// we always use first container in a pod

	name := pod.ObjectMeta.Name
	namespace := pod.ObjectMeta.Namespace
	port := pod0.Ports[0].ContainerPort
	if port == 0 && phase != PhaseDEL {
		err = fmt.Errorf("hook name: %v got empty port\n", name)
		return
	}

	//log.Println("ip ", ip, port, name)

	return PodInfo{
		Name:      name,
		Namespace: namespace,
		Phase:     phase,
		IP:        ip,
		Port:      strconv.Itoa(int(port)),
		Reason:    reason,
		Msg:       msg,
	}, nil
}

// should ignore pending and empty ip
func hook(phase Phase, pods ...*v1.Pod) {
	defer func() {
		if e := recover(); e != nil {
			log.Println("found error in hook: ", e)
		}
	}()

	var pod, oldpod *v1.Pod

	if len(pods) == 0 || len(pods) > 2 {
		log.Println("hook function call error, skip")
		return
	}

	if len(pods) == 1 {
		pod = pods[0]
	}
	if len(pods) == 2 {
		pod = pods[0]
		oldpod = pods[1]
	}
	//log.Printf("dump %v\n", spew.Sdump("new: %v, ==== old:%v\n", pod, oldpod))

	log.Printf("hook meta: %v, ns: %v, status: %v, phase: %v, skip: %v\n",
		pod.ObjectMeta.Name, pod.ObjectMeta.Namespace, pod.Status.Phase, phase, phase != PhaseUPDATE)

	if phase != PhaseUPDATE {
		// ignore add and del event, use update only instead
		return
	}

	p, err := parsePod(phase, pod)
	if err != nil {
		log.Println("hook parse pod err: ", err)
		return
	}
	//log.Printf("podinfo: %#v\n", p)

	// for testing, to comment out
	/* 	if !strings.Contains(p.Name, "ops-fs") {
	   		log.Println("hook skip non ops-fs for now")
	   		return
	   	}
	*/
	//log.Printf("info: new:%#v,old:%#v\nend...\n", pod, oldpod)

	var realPhase Phase
	var realip, realport string
	if phase == PhaseUPDATE {
		oldpodinfo, err := parsePod(phase, oldpod)
		if err != nil {
			log.Println("hook parse oldpod err: ", err)
			return
		}

		// the pattern of delete pod
		if (pod.ObjectMeta.DeletionTimestamp != nil) && (oldpod.ObjectMeta.DeletionTimestamp == nil) {
			log.Println("found a delete phase")
			realip = p.IP
			realport = p.Port
			realPhase = PhaseDEL
		}

		// this is correct, but wait 30 seconds
		/* 	if (oldpodinfo.IP != "" && p.IP == "") || (oldpodinfo.Port != p.Port) {
			realip = oldpodinfo.IP
			realport = oldpodinfo.Port
			realPhase = PhaseDEL
		} */

		// pattern of add
		if (oldpodinfo.IP == "" && p.IP != "") || (oldpodinfo.Port != p.Port) {
			log.Println("found a add phase")
			realip = p.IP
			realport = p.Port
			realPhase = PhaseADD
		}

		if oldpodinfo.IP != p.IP {
			log.Printf("hook old ip: %v:%v, new ip: %v:%v\n", oldpodinfo.IP, oldpodinfo.Port, p.IP, p.Port)
		}

	}
	if realPhase == PhaseUnknown {
		log.Println("not detect as del or add, so skip this phase")
		return
	}

	log.Printf("hook container name %v, realip: %v:%v, realphase: %v\n", p.Name, realip, realport, realPhase)

	if p.Reason != "" || p.Msg != "" {
		log.Printf("hook realphase: %v, realip: %v:%v, reason: %v, msg: %v\n", realPhase, realip, realport, p.Reason, p.Msg)
	}

	go func() {
		log.Printf("hook start called upstream for ns: %v, name: %v\n", p.Namespace, p.Name)
		err := CallHookAPI(phase2state(realPhase), p.Name, p.Namespace, realip, realport, p.Reason, p.Msg)
		if err != nil {
			log.Printf("called hookapi for name: %v, error: %v\n", p.Name, err)
			return
		}
		log.Printf("called hookapi for name: %v ok\n", p.Name)
	}()
}

func convertPod(s v1.PodStatus) (ip, reason, msg string) {
	return s.PodIP, s.Reason, s.Message
}

func phase2state(phase Phase) string {
	return fmt.Sprintf("%v", phase)
}

func CallHookAPI(phase, name, ns, ip, port, reason, msg string) error {
	resp, err := resty.R().
		SetFormData(map[string]string{
			"podname":   name,
			"namespace": ns,
			"ip":        ip,
			"port":      port,
			"state":     phase, // int 1:up or 0:down
		}).
		Post(*hookAPI)

	if err != nil {
		return err
	}
	if resp.StatusCode() != http.StatusOK {
		log.Printf("hook call hookapi error code: %v, body:%v\n", resp.StatusCode(), string(resp.Body()))
	}
	return nil
}
