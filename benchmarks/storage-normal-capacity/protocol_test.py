"""Written synthetic controls; real subprocess/network/SQL work is forbidden."""
import copy
import importlib
import hashlib
import os
from pathlib import Path
import socket
import subprocess
import unittest
from unittest import mock
import urllib.request

ATTEMPTS = 0

def forbidden(*args, **kwargs):
    global ATTEMPTS
    ATTEMPTS += 1
    raise AssertionError("actual child/network operation forbidden")

GUARDS = [mock.patch.object(subprocess, "Popen", forbidden), mock.patch.object(urllib.request, "urlopen", forbidden), mock.patch.object(socket, "create_connection", forbidden), mock.patch.object(socket.socket, "connect", forbidden), mock.patch.object(socket.socket, "connect_ex", forbidden), mock.patch.object(os, "system", forbidden)]
for name in ("posix_spawn", "posix_spawnp"):
    if hasattr(os, name): GUARDS.append(mock.patch.object(os, name, forbidden))
for guard in GUARDS: guard.start()

from protocol import (Refused, admitted_sizes, canonical, loads, validate_identity,
                      validate_normal_stage, validate_go_json)

IDENTITY = loads(Path(__file__).with_name("identity.json").read_bytes())

def fixture(api=False, size=1000, schema=5):
    paths = ["/owned/artifacts/normal-scan-%06d" % i for i in range(size+1024)]
    sizes = [9]*1024+[0]*size
    healthy = ["/owned/artifacts/retained-%d" % i for i in range(1,size+1) if i%100!=1]
    rows = {"sources":[{"id":"source-%d" % i} for i in range(size)], "jobs":[{"id":"job-%d" % i} for i in range(size)], "artifacts":[{"id":"artifact-%d" % i} for i in range(size-size//100)], "previews":[{"id":"preview-%d" % i} for i in range(200)]}
    def state(charges, complete=False, reservation=0):
        return {"committed_path_charges":charges,"protected_rows_sha256":hashlib.sha256(canonical(rows)).hexdigest(),"retained_sources":size,"retained_jobs":size,"healthy_artifacts":size-size//100,"preview_pins":200,"bad_derived_pins":0,"bad_healthy_charges":0,"healthy_path_charges":dict.fromkeys(healthy,16),"bad_path_charges":0,"physical_files_intact":True,"stored_bytes":16*(size-size//100)+sum(charges.values()),"file_ledger_bytes":16*(size-size//100)+sum(charges.values()),"reserved_bytes":3200+reservation*4096,"reservation_ledger_bytes":3200+reservation*4096,"initialization_markers":int(complete),"completed_scan_kinds":(3 if api else 5) if complete else 0,"owned_api_reservation":reservation,"oracle_ms":1.0}
    expected = dict(zip(paths,sizes));budget=5000 if api else 10000
    first=state(dict(list(expected.items())[:128]))
    last=state(expected,True,int(api))
    calls=[]
    for i,(observed,done) in enumerate(((first,False),(last,True)),1):
        calls.append({"call":i,"caller_budget_ms":budget,"effective_deadline_ms":budget-.1,"wall_ms":1.0,"outcome":"acknowledged" if done else "initializing_pending","parent_expired":False,"acknowledged":done,"error_type":"<nil>" if done else "*errors.errorString","committed_progress_monotonic":True,"protected_pass":True,"completed":done,"independent_reopened_state":observed})
    return {"pass":True,"profile":"normal10s5s","entrypoint":"ReserveSource_database_admission" if api else "RunMaintenance_one_shot","caller_budget_ms":budget,"maximum_calls":8,"calls":calls,"initial":state({}),"final":state(expected,True),"runtime_role":{"pass":True},"expected_healthy_paths":healthy,"expected_scan_paths":paths,"expected_scan_sizes":sizes,"expected_protected_physical_files":size-size//100,"expected_scan_physical_files":size+1024,"fresh_independent_fixture":True,"unbounded_repair_calls":0,"derived_pin_validation_applicable":schema==5,"release_acknowledged":True,"complete":True,"service_SLA_claimed":False}

class NormalProfileControls(unittest.TestCase):
    @classmethod
    def tearDownClass(cls):
        try:
            if ATTEMPTS != 0: raise AssertionError("actual child/network attempt")
            print("NORMAL PROFILE PURE CONTROL actual_child_network_attempts=0",flush=True)
        finally:
            for guard in reversed(GUARDS): guard.stop()

    def reject(self,value,api=False):
        with self.assertRaises(Refused): validate_normal_stage(value,5000 if api else 10000,api,1000,5)

    def test_fixed_current_app173_and_before004_identity(self):
        self.assertEqual(len(validate_identity(IDENTITY)["versions"]["after"]["application_files"]),173)
        for key,value in (("commit","0"*40),("tree","0"*40),("parents",[]),("application_inventory_sha256","0"*64)):
            d=copy.deepcopy(IDENTITY);d["versions"]["after"][key]=value
            with self.subTest(key=key),self.assertRaises(Refused):validate_identity(d)

    def test_both_budgets_acknowledged_reopened_charge_completion(self):
        for api in (False,True):
            for schema in (4,5):
                with self.subTest(api=api,schema=schema):self.assertTrue(validate_normal_stage(fixture(api,schema=schema),5000 if api else 10000,api,1000,schema))

    def test_deadline_pending_never_claims_completion(self):
        d=fixture();c=d["calls"][0];c.update(outcome="caller_deadline_pending",parent_expired=True)
        self.assertTrue(validate_normal_stage(d,10000,False,1000,5))
        c["completed"]=True;self.reject(d)

    def test_genuine_error_preserved_as_newprofile_failure(self):
        d=fixture();d["calls"]=d["calls"][:1];c=d["calls"][0];c.update(outcome="genuine_error",parent_expired=True,error_type="*pgconn.PgError");d.update({"pass":False,"complete":False,"final":copy.deepcopy(c["independent_reopened_state"])})
        self.assertFalse(validate_normal_stage(d,10000,False,1000,5))
        d["pass"]=True;self.reject(d)

    def test_deadline_without_expired_parent_refuses(self):
        d=fixture();d["calls"][0]["outcome"]="caller_deadline_pending";self.reject(d)

    def test_late_ack_cannot_produce_normal_completion(self):
        d=fixture();d["calls"][-1]["parent_expired"]=True;self.reject(d)

    def test_intermediate_late_ack_cannot_be_repaired_by_good_ack(self):
        d=fixture();c=d["calls"][0]
        c.update(outcome="late_acknowledged",parent_expired=True,acknowledged=True,error_type="<nil>")
        self.reject(d)  # A subsequent healthy ACK cannot erase this failed call.
        d["calls"]=d["calls"][:1]
        d.update({"pass":False,"complete":False,"final":copy.deepcopy(c["independent_reopened_state"])})
        self.assertFalse(validate_normal_stage(d,10000,False,1000,5))

    def test_committed_prefix_disappearance_and_offsetting_charges_refuse(self):
        for offset in (False,True):
            d=fixture();charges=d["calls"][-1]["independent_reopened_state"]["committed_path_charges"];names=list(charges)
            if offset:charges[names[0]]-=1;charges[names[1]]+=1
            else:del charges[names[0]]
            with self.subTest(offset=offset):self.reject(d)

    def test_protected_row_pin_and_physical_corruption_cannot_pass(self):
        for key,value in (("protected_rows_sha256","0"*64),("bad_derived_pins",1),("physical_files_intact",False),("file_ledger_bytes",99999)):
            d=fixture();d["calls"][-1]["independent_reopened_state"][key]=value
            with self.subTest(key=key):self.reject(d)

    def test_healthy_charge_compensation_refuses_even_equal_total(self):
        d=fixture();charges=d["calls"][-1]["independent_reopened_state"]["healthy_path_charges"];paths=list(charges);charges[paths[0]]=8;charges[paths[1]]=24;self.reject(d)

    def test_final_reopen_regression_or_unreleased_api_reservation_refuses(self):
        d=fixture();del d["final"]["committed_path_charges"][d["expected_scan_paths"][0]];self.reject(d)
        d=fixture(True);d["final"]["owned_api_reservation"]=1;self.reject(d,True)

    def test_more_calls_unbounded_repair_or_legacy_profile_refuse(self):
        for key,value in (("maximum_calls",9),("unbounded_repair_calls",1),("profile","legacy5ms"),("service_SLA_claimed",True)):
            d=fixture();d[key]=value
            with self.subTest(key=key):self.reject(d)

    def test_missing_noninteger_charges_and_duplicate_paths_refuse(self):
        for kind in ("missing","float","duplicate"):
            d=fixture()
            if kind=="missing":del d["calls"][0]["independent_reopened_state"]["committed_path_charges"]
            elif kind=="float":d["final"]["committed_path_charges"][d["expected_scan_paths"][0]]=9.0
            else:d["expected_scan_paths"][-1]=d["expected_scan_paths"][0]
            with self.subTest(kind=kind):self.reject(d)

    def test_100k_unknown_size_and_runtime_imports_remain_refused_or_inert(self):
        self.assertEqual(admitted_sizes("1000"),[1000]);self.assertEqual(admitted_sizes("10000"),[1000,10000])
        for value in ("100000",1000,"1000.0",None):
            with self.subTest(value=value),self.assertRaises(Refused):admitted_sizes(value)
        importlib.import_module("run_hosted");importlib.import_module("verify_saved")
        self.assertEqual(ATTEMPTS,0)

    def test_go_terminal_cannot_borrow_old_profile_test(self):
        name="TestStorageNormalCapacityPilot";package="github.com/Kay0k1/cutmyvideo-core/internal/app"
        events=[{"Action":"run","Package":package,"Test":name},{"Action":"pass","Package":package,"Test":name},{"Action":"pass","Package":package}];raw=b"".join(canonical(x).replace(b"\n",b" ").strip()+b"\n" for x in events)
        self.assertTrue(validate_go_json(raw,0,True))
        with self.assertRaises(Refused):validate_go_json(raw.replace(name.encode(),b"TestStorage005ComparisonPilot"),0,True)
