package dbmcp

import "testing"

func TestEngineFor(t *testing.T) {
	for awsEngine, want := range map[string]Engine{
		"postgres": EnginePostgres, "aurora-postgresql": EnginePostgres, "sqlserver-se": EngineSQLServer,
		"mysql": EngineMySQL, "aurora-mysql": EngineMySQL, "aurora": EngineMySQL, "MariaDB": EngineMariaDB,
		"oracle-ee": "",
	} {
		if got, ok := EngineFor(awsEngine); got != want || ok != (want != "") {
			t.Errorf("EngineFor(%q) = %q, %v; want %q", awsEngine, got, ok, want)
		}
	}
}
