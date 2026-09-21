// Command relay-characterization-taxonomy exports the shared engine vocabulary.
package main

import (
	"encoding/json"
	"os"
	"github.com/anchorshell/relay/pkg/characterization"
)

func main(){
	if err:=json.NewEncoder(os.Stdout).Encode(struct{
		Hash string `json:"hash"`
		Actions []characterization.Action `json:"actions"`
		Objects []characterization.Object `json:"objects"`
		Domains []characterization.Domain `json:"domains"`
	}{characterization.TaxonomyHash(),characterization.TrainedActions,characterization.Objects,characterization.Domains});err!=nil{os.Exit(1)}
}
