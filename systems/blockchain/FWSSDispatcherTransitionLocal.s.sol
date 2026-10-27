// SPDX-License-Identifier: Apache-2.0 OR MIT
pragma solidity 0.8.37;

// upgrade-contracts.sh copies this script into a filecoin-services checkout (v1.5.0 or later) as
// service_contracts/script/FWSSDispatcherTransitionLocal.s.sol and runs it there.

import {Script, console} from "forge-std/Script.sol";
import {Constructor} from "@erc8167/lib/Constructor.sol";
import {Migration, SetDelegateOperation, SetDelegateOperationLibrary} from "@erc8167/lib/Migration.sol";
import {ERC1967Utils} from "@openzeppelin/contracts/proxy/ERC1967/ERC1967Utils.sol";
import {FWSSDispatcherTransition} from "../src/FWSSDispatcherTransition.sol";
import {FWSS_DISPATCHER_ARTIFACT} from "./lib/FWSSDeployer.sol";
import {JosukeFacetSet} from "./lib/JosukeFacetSet.sol";

/// @notice Deploys the FWSS modules that josuke.json lists, their migration, the pinned ERC-8167 dispatcher and an
/// FWSSDispatcherTransition for an existing v1.4.0 proxy, without the josuke CLI. The proxy's owner then announces the
/// transition and calls upgradeToAndCall(transition, migrate(migration)).
contract FWSSDispatcherTransitionLocal is Script, JosukeFacetSet {
    function run() external {
        address proxy = vm.envAddress("FWSS");
        _setConstructorArg("_paymentsContractAddress", vm.envAddress("FILECOIN_PAY"));
        _setConstructorArg("_pdpVerifierAddress", vm.envAddress("PDP_VERIFIER"));
        _setConstructorArg("_usdfc", vm.envAddress("USDFC"));
        _setConstructorArg("_filBeamBeneficiaryAddress", vm.envAddress("FILBEAM_BENEFICIARY"));
        _setConstructorArg("_serviceProviderRegistry", vm.envAddress("SERVICE_PROVIDER_REGISTRY"));
        _setConstructorArg("_sessionKeyRegistry", vm.envAddress("SESSION_KEY_REGISTRY"));
        address previous = address(uint160(uint256(vm.load(proxy, ERC1967Utils.IMPLEMENTATION_SLOT))));
        Facet[] memory facets = _resolveFacets(MAINNET_INDEX);

        vm.startBroadcast();
        SetDelegateOperation[] memory routes = _deployFacetRoutes(facets);
        SetDelegateOperationLibrary.validate(routes);
        address migration = Migration.createMigration(routes);
        address dispatcher = Constructor.create(vm.getCode(FWSS_DISPATCHER_ARTIFACT));
        FWSSDispatcherTransition transition = new FWSSDispatcherTransition(previous, dispatcher, migration);
        vm.stopBroadcast();

        console.log("dispatcher", dispatcher);
        console.log("migration", migration);
        console.log("transition", address(transition));
    }
}
