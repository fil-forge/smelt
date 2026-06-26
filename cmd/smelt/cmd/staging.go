package cmd

import (
	"fmt"

	"github.com/fil-forge/smelt/pkg/staging"
	"github.com/spf13/cobra"
)

var stagingCmd = &cobra.Command{
	Use:   "staging",
	Short: "Manage the Forge staging deployment",
}

var stagingKeygenCmd = &cobra.Command{
	Use:   "keygen",
	Short: "One-time generation of staging keys, wallets, and proofs",
	Long: `Generates the staging stack's long-lived secrets ONCE and stores them in
1Password, and writes the (non-secret) UCAN delegation proofs into
environments/staging/proofs/ to be committed.

It generates:
  - Ed25519 service identity keys (PEM)
  - real random secp256k1 EVM wallets for the payer, delegator transactor, and
    piri owner — their addresses are printed so you can fund them via a Calibnet
    faucet (private keys are stored in 1Password, never printed)
  - random Postgres/MinIO connection secrets
  - the indexing/egress/piri UCAN delegation proofs

This is a deliberate one-shot: re-running mints fresh keys and overwrites the
1Password item, orphaning any already-funded wallets. Run it again only when
intentionally rotating the whole staging identity set.`,
	RunE: runStagingKeygen,
}

func init() {
	rootCmd.AddCommand(stagingCmd)
	stagingCmd.AddCommand(stagingKeygenCmd)
	stagingKeygenCmd.Flags().StringP("project-dir", "d", ".", "project root directory")
	stagingKeygenCmd.Flags().String("op-vault", "Fil One", "1Password vault")
	stagingKeygenCmd.Flags().String("op-item", "FilOne Forge Staging", "1Password item title")
	stagingKeygenCmd.Flags().Bool("store", true, "store generated secrets into 1Password via the op CLI")
	stagingKeygenCmd.Flags().Bool("proofs", true, "generate UCAN delegation proofs (requires ucantool)")
	stagingKeygenCmd.Flags().String("ucantool", "ucantool", "ucantool binary name or path")
}

func runStagingKeygen(cmd *cobra.Command, args []string) error {
	projectDir, _ := cmd.Flags().GetString("project-dir")
	opVault, _ := cmd.Flags().GetString("op-vault")
	opItem, _ := cmd.Flags().GetString("op-item")
	store, _ := cmd.Flags().GetBool("store")
	proofs, _ := cmd.Flags().GetBool("proofs")
	ucantool, _ := cmd.Flags().GetString("ucantool")

	result, err := staging.Keygen(staging.Options{
		ProjectDir: projectDir,
		OPVault:    opVault,
		OPItem:     opItem,
		Store:      store,
		Proofs:     proofs,
		Ucantool:   ucantool,
	})
	if err != nil {
		return err
	}

	fmt.Println("Staging keygen complete.")
	if len(result.ProofsWritten) > 0 {
		fmt.Printf("\nProofs written (commit these):\n")
		for _, p := range result.ProofsWritten {
			fmt.Printf("  %s\n", p)
		}
	}
	if len(result.OPFields) > 0 {
		fmt.Printf("\nStored %d secret field(s) in 1Password item %q (vault %q):\n",
			len(result.OPFields), opItem, opVault)
		for _, f := range result.OPFields {
			fmt.Printf("  %s\n", f)
		}
	}
	if result.WalletsEnvPath != "" {
		fmt.Printf("\nWrote wallet addresses to %s (commit it).\n", result.WalletsEnvPath)
	}
	fmt.Printf("\nFund these wallets on the Calibnet faucet (https://faucet.calibnet.chainsafe-fil.io):\n")
	for role, addr := range result.FundAddresses {
		fmt.Printf("  %-24s %s\n", role+":", addr)
	}
	return nil
}
