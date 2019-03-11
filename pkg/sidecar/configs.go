/*
Copyright 2019 Pressinfra SRL

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

package sidecar

import (
	"fmt"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/go-ini/ini"
	// add mysql driver
	_ "github.com/go-sql-driver/mysql"

	"github.com/presslabs/mysql-operator/pkg/internal/mysqlcluster"
	orc "github.com/presslabs/mysql-operator/pkg/orchestrator"
)

// NodeRole represents the kind of the MySQL server
type NodeRole string

const (
	// MasterNode represents the master role for MySQL server
	MasterNode NodeRole = "master"
	// SlaveNode represents the slave role for MySQL server
	SlaveNode NodeRole = "slave"
)

// Config contains information related with the pod.
type Config struct {
	// Hostname represents the pod hostname
	Hostname string
	// ClusterName is the MySQL cluster name
	ClusterName string
	// Namespace represents the namespace where the pod is in
	Namespace string
	// ServiceName is the name of the headless service
	ServiceName string

	// ServerID represents the MySQL server id
	ServerID int

	// InitBucketURL represents the init bucket to initialize mysql
	InitBucketURL string

	// OrchestratorURL is the URL to connect to orchestrator
	OrchestratorURL string

	// backup user and password for http endpoint
	BackupUser     string
	BackupPassword string

	// MysqlDSN represents the connection string to connect to MySQL
	MysqlDSN string

	// replication user and password
	ReplicationUser     string
	ReplicationPassword string

	// metrcis exporter user and password
	MetricsUser     string
	MetricsPassword string

	// orchestrator credentials
	OrchestratorUser     UpdatableString
	OrchestratorPassword UpdatableString

	// MySQL and app related credentials
	MysqlRootPassword UpdatableString
	MysqlUser         UpdatableString
	MysqlPassword     UpdatableString
	MysqlDatabase     UpdatableString

	ReconcileTime time.Duration
}

// FQDNForServer returns the pod hostname for given MySQL server id
func (cfg *Config) FQDNForServer(id int) string {
	base := mysqlcluster.GetNameForResource(mysqlcluster.StatefulSet, cfg.ClusterName)
	return fmt.Sprintf("%s-%d.%s.%s", base, id-MysqlServerIDOffset, cfg.ServiceName, cfg.Namespace)
}

func (cfg *Config) newOrcClient() orc.Interface {
	if len(cfg.OrchestratorURL) == 0 {
		log.Info("OrchestratorURL not set")
		return nil
	}

	return orc.NewFromURI(cfg.OrchestratorURL)
}

// ClusterFQDN returns the cluster FQ Name of the cluster from which the node belongs
func (cfg *Config) ClusterFQDN() string {
	return fmt.Sprintf("%s.%s", cfg.ClusterName, cfg.Namespace)
}

// MasterFQDN the FQ Name of the cluster's master
func (cfg *Config) MasterFQDN() string {
	if client := cfg.newOrcClient(); client != nil {
		if master, err := client.Master(cfg.ClusterFQDN()); err == nil {
			return master.Key.Hostname
		}
	}

	log.V(-1).Info("failed to obtain master from orchestrator, go for default master",
		"master", cfg.FQDNForServer(100))
	return cfg.FQDNForServer(100)

}

// NodeRole returns the role of the current node
func (cfg *Config) NodeRole() NodeRole {
	if cfg.Hostname == cfg.MasterFQDN() {
		return MasterNode
	}

	return SlaveNode
}

// NewConfig returns a pointer to Config configured from environment variables
func NewConfig() *Config {
	cfg := &Config{
		Hostname:    getEnvValue("HOSTNAME"),
		ClusterName: getEnvValue("MY_CLUSTER_NAME"),
		Namespace:   getEnvValue("MY_NAMESPACE"),
		ServiceName: getEnvValue("MY_SERVICE_NAME"),

		InitBucketURL:   getEnvValue("INIT_BUCKET_URI"),
		OrchestratorURL: getEnvValue("ORCHESTRATOR_URI"),

		BackupUser:     getEnvValue("MYSQL_BACKUP_USER"),
		BackupPassword: getEnvValue("MYSQL_BACKUP_PASSWORD"),

		ReplicationUser:     getEnvValue("MYSQL_REPLICATION_USER"),
		ReplicationPassword: getEnvValue("MYSQL_REPLICATION_PASSWORD"),

		MetricsUser:     getEnvValue("MYSQL_METRICS_EXPORTER_USER"),
		MetricsPassword: getEnvValue("MYSQL_METRICS_EXPORTER_PASSWORD"),

		OrchestratorUser:     GetValueFromFile(absPath("ORC_TOPOLOGY_USER")),
		OrchestratorPassword: GetValueFromFile(absPath("ORC_TOPOLOGY_PASSWORD")),

		MysqlUser:         GetValueFromFile(absPath("USER")),
		MysqlPassword:     GetValueFromFile(absPath("PASSWORD")),
		MysqlDatabase:     GetValueFromFile(absPath("DATABASE")),
		MysqlRootPassword: GetValueFromFile(absPath("ROOT_PASSWORD")),

		ReconcileTime: 10 * time.Second,
	}

	// get server id
	ordinal := getOrdinalFromHostname(cfg.Hostname)
	cfg.ServerID = ordinal + 100

	var err error
	if cfg.MysqlDSN, err = getMySQLConnectionString(); err != nil {
		log.Error(err, "get mysql dsn")
	}

	return cfg
}

func getEnvValue(key string) string {
	value := os.Getenv(key)
	if len(value) == 0 {
		log.Info("envirorment is not set", "key", key)
	}

	return value
}

func absPath(key string) string {
	return path.Join(secretMountPath, key)
}

func getOrdinalFromHostname(hn string) int {
	// mysql-master-1
	// or
	// stateful-ceva-3
	l := strings.Split(hn, "-")
	for i := len(l) - 1; i >= 0; i-- {
		if o, err := strconv.ParseInt(l[i], 10, 8); err == nil {
			return int(o)
		}
	}

	return 0
}

// getMySQLConnectionString returns the mysql DSN
func getMySQLConnectionString() (string, error) {
	cnfPath := path.Join(configDir, "client.cnf")
	cfg, err := ini.Load(cnfPath)
	if err != nil {
		return "", fmt.Errorf("Could not open %s: %s", cnfPath, err)
	}

	client := cfg.Section("client")
	host := client.Key("host").String()
	user := client.Key("user").String()
	password := client.Key("password").String()
	port, err := client.Key("port").Int()
	if err != nil {
		return "", fmt.Errorf("Invalid port in %s: %s", cnfPath, err)
	}

	dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/?timeout=5s&multiStatements=true&interpolateParams=true",
		user, password, host, port,
	)
	return dsn, nil
}
