//
package main

import (
	"fmt"
	"net/http"
	"runtime"
	"time"

	"crypto/md5"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/client_golang/prometheus/push"
	"github.com/prometheus/common/log"
	"gopkg.in/alecthomas/kingpin.v2"
	"os"
	"sync"
)

const (
	pgSCVVersion = "0.0.5"
)

var (
	promPushGw       = kingpin.Flag("prom.pushgateway", "Pushgateway address push to").Default("").Envar("PROM_PUSHGATEWAY").String()
	promPushInterval = kingpin.Flag("prom.pushinterval", "Interval between pushes").Default("10s").Envar("PROM_PUSHINTERVAL").Duration()
	cfId             = kingpin.Flag("cfid", "Cluster family identificator, must be the same over the master and all its standbys").Envar("PGSCV_CFID").String()

	wg            sync.WaitGroup
	chStartListen = make(chan int8)
	useSchedule   bool
)

func main() {
	log.AddFlags(kingpin.CommandLine)
	kingpin.Version(fmt.Sprintf("pgscv exporter %s (built with %s)", pgSCVVersion, runtime.Version()))
	kingpin.Parse()

	// обязательно должен быть
	if *cfId == "" {
		log.Fatalln("global system identifier must be specified.")
	}
	// use schedulers in push mode
	if *promPushGw != "" {
		useSchedule = true
	}

	wg.Add(1)
	go discoveryLoop()

	<-chStartListen

	// TODO: унести содержимое в функции в prometheus.go
	if *promPushGw == "" {
		log.Infof("use PULL model, accepting requests on http://127.0.0.1:19090/metrics")

		http.Handle("/metrics", promhttp.Handler())
		if err := http.ListenAndServe("127.0.0.1:19090", nil); err != nil { // TODO: дефолтный порт должен быть другим
			log.Fatalln(err)
		}
	} else {
		log.Infof("use PUSH model, sending metrics to %s every %d seconds", *promPushGw, *promPushInterval/time.Second)
		hostname, _ := os.Hostname()
		var garbageLabel = "db_system_" + fmt.Sprintf("%x", md5.Sum([]byte(hostname)))
		var pusher *push.Pusher

		for {
			// A garbage label is the special one which provides metrics uniqueness across several hosts and guarantees
			// metrics will not be overwritten on Pushgateway side. There is no other use-cases for this label, hence
			// before ingesting by Prometheus this label should be removed with 'metric_relabel_config' rule.
			pusher = push.New(*promPushGw, garbageLabel)
			for i := range Instances {
				pusher.Collector(Instances[i].Worker)
			}

			if err := pusher.Add(); err != nil {
				log.Errorf("%s: could not push metrics: %s", time.Now().Format("2006-01-02T15:04:05.999"), err)
			}
			time.Sleep(*promPushInterval)
		}
	}

	wg.Wait()
	log.Infoln("Done. Exit.")
}
