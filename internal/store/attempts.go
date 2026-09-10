package store

import "fmt"

// AttemptFailureSQL is shared by alert evidence and request-log filters.
// Arguments are trusted SQL aliases, never user input.
func AttemptFailureSQL(attempt, finalError string) string {
	return fmt.Sprintf(`((COALESCE((%[1]s->>'status_code')::int,0)=0 OR (%[1]s->>'status_code')::int IN (401,403,404,429) OR (%[1]s->>'status_code')::int>=500 OR COALESCE(%[1]s->>'error','')<>'')
 AND COALESCE(%[1]s->>'failure_class','') NOT IN ('client','gateway')
 AND NOT (COALESCE(%[1]s->>'failure_class','')='' AND %[2]s='client_closed' AND (COALESCE((%[1]s->>'status_code')::int,0)=0 OR (%[1]s->>'status_code')::int BETWEEN 200 AND 399)))`, attempt, finalError)
}
