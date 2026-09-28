#!/usr/bin/env python3
"""Unit tests for the dependency-free RSpec discovery helpers."""

import importlib.util
import pathlib
import unittest


MODULE_PATH = pathlib.Path(__file__).with_name("cloudlab_nodetypes.py")
SPEC = importlib.util.spec_from_file_location("cloudlab_nodetypes", MODULE_PATH)
DISCOVERY = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(DISCOVERY)

RSPEC = """\
<rspec xmlns="http://www.geni.net/resources/rspec/3">
  <node component_manager_id="urn:publicid:IDN+example.test+cm" exclusive="true">
    <hardware_type name="c220g5" />
    <sliver_type name="raw-pc" />
    <available now="true" />
  </node>
  <node component_manager_id="urn:publicid:IDN+example.test+cm" exclusive="true">
    <hardware_type name="c220g5" />
    <sliver_type name="raw-pc" />
    <available now="false" />
  </node>
  <node component_manager_id="urn:publicid:IDN+example.test+cm" exclusive="true">
    <hardware_type name="switch" />
    <available now="true" />
  </node>
  <node component_manager_id="urn:publicid:IDN+example.test+cm" exclusive="true">
    <hardware_type name="ipv4" />
    <available now="true" />
  </node>
</rspec>
"""


class DiscoveryTests(unittest.TestCase):
    def test_collect_keeps_only_raw_pc_nodes_by_default(self):
        result = DISCOVERY.collect(RSPEC)

        self.assertEqual(
            result,
            {
                (
                    "urn:publicid:IDN+example.test+authority+cm",
                    "c220g5",
                ): [1, 2]
            },
        )

    def test_collect_can_include_noncompute_nodes_for_diagnostics(self):
        result = DISCOVERY.collect(RSPEC, include_noncompute=True)

        self.assertEqual(
            result[
                ("urn:publicid:IDN+example.test+authority+cm", "switch")
            ],
            [1, 1],
        )
        self.assertEqual(
            result[("urn:publicid:IDN+example.test+authority+cm", "ipv4")],
            [1, 1],
        )

    def test_default_source_uses_all_six_portal_aggregates(self):
        args = DISCOVERY.parse_args([])

        self.assertEqual(args.source, "auto")
        self.assertIsNone(args.aggregates)
        self.assertEqual(
            DISCOVERY.DEFAULT_PORTAL_AGGREGATES,
            ("Utah", "Wisconsin", "Clemson", "Apt", "Emulab", "Mass"),
        )

    def test_normalize_urn_preserves_already_normalized_value(self):
        urn = "urn:publicid:IDN+wisc.cloudlab.us+authority+cm"

        self.assertEqual(DISCOVERY.normalize_urn(urn), urn)

    def test_parse_portal_inventory_filters_noncompute_and_converts_counts(self):
        urn = DISCOVERY.PORTAL_AGGREGATES["Utah"]
        cpu_attributes = {"architecture": "x86_64", "hw_cpu_cores": "16"}
        payload = {
            "code": 0,
            "value": [
                {},
                {
                    urn: {
                        "typelist": {
                            "compute": cpu_attributes,
                            "control-infra": cpu_attributes,
                            "switch": None,
                        },
                        "typeinfo": {
                            "compute": {"count": "10", "free": "3"},
                            "control-infra": {"count": "1", "free": "0"},
                            "switch": {"count": "2", "free": "0"},
                        },
                    }
                },
            ],
        }

        rows, errors = DISCOVERY.parse_portal_inventory(payload, ["Utah"])

        self.assertEqual(errors, {})
        self.assertEqual(
            rows,
            [
                {
                    "cluster": "Utah",
                    "urn": urn,
                    "node_type": "compute",
                    "free": 3,
                    "total": 10,
                }
            ],
        )

    def test_parse_portal_inventory_reports_missing_aggregate(self):
        rows, errors = DISCOVERY.parse_portal_inventory(
            {"code": 0, "value": [{}, {}]}, ["Mass"]
        )

        self.assertEqual(rows, [])
        self.assertIn("missing", errors["Mass"])


class _FakeAggregate:
    def __init__(self, text=None, error=None, delay=0.0):
        self._text = text
        self._error = error
        self._delay = delay

    def listresources(self, _context):
        if self._delay:
            import time

            time.sleep(self._delay)
        if self._error is not None:
            raise self._error

        class Manifest:
            text = self._text

        return Manifest()


class DiscoverAggregatesTests(unittest.TestCase):
    def test_collects_results_errors_and_timeouts(self):
        catalog = {
            "Fast": _FakeAggregate(text="<rspec/>"),
            "Broken": _FakeAggregate(error=RuntimeError("boom")),
            "Stuck": _FakeAggregate(text="<rspec/>", delay=5.0),
        }

        rspecs, errors = DISCOVERY.discover_aggregates(
            ["Fast", "Broken", "Stuck"], catalog, context=None, timeout=0.5
        )

        self.assertEqual(rspecs, {"Fast": "<rspec/>"})
        self.assertIn("boom", errors["Broken"])
        self.assertIn("timed out", errors["Stuck"])

    def test_runs_aggregates_in_parallel(self):
        import time

        catalog = {
            name: _FakeAggregate(text="<rspec/>", delay=0.3)
            for name in ("A", "B", "C", "D")
        }

        started = time.monotonic()
        rspecs, errors = DISCOVERY.discover_aggregates(
            list(catalog), catalog, context=None, timeout=5.0
        )
        elapsed = time.monotonic() - started

        self.assertEqual(len(rspecs), 4)
        self.assertEqual(errors, {})
        # Serial would need 4 x 0.3s; parallel should finish in ~0.3s.
        self.assertLess(elapsed, 0.9)

    def test_timeout_flag_defaults_to_thirty_seconds(self):
        args = DISCOVERY.parse_args([])

        self.assertEqual(args.timeout, 30.0)

    def test_connect_timeout_defaults_to_five_seconds(self):
        args = DISCOVERY.parse_args([])

        self.assertEqual(args.connect_timeout, 5.0)

    def test_on_rspec_callback_fires_per_aggregate(self):
        calls = []
        catalog = {
            "A": _FakeAggregate(text="<a/>"),
            "B": _FakeAggregate(text="<b/>"),
        }

        rspecs, errors = DISCOVERY.discover_aggregates(
            ["A", "B"], catalog, context=None, timeout=5.0,
            on_rspec=lambda name, text: calls.append((name, text)),
        )

        self.assertEqual(errors, {})
        self.assertEqual(len(rspecs), 2)
        self.assertEqual(sorted(calls), [("A", "<a/>"), ("B", "<b/>")])

    def test_on_rspec_exception_marks_aggregate_failed(self):
        catalog = {"A": _FakeAggregate(text="<a/>")}

        def broken_emit(name, text):
            raise RuntimeError("emit failed")

        rspecs, errors = DISCOVERY.discover_aggregates(
            ["A"], catalog, context=None, timeout=5.0, on_rspec=broken_emit
        )

        self.assertEqual(rspecs, {})
        self.assertIn("emit failed", errors["A"])

    def test_late_worker_cannot_mutate_results_or_emit_after_timeout(self):
        import time

        calls = []
        rspecs, errors = DISCOVERY.discover_aggregates(
            ["Slow"],
            {"Slow": _FakeAggregate(text="<late/>", delay=0.08)},
            context=None,
            timeout=0.01,
            on_rspec=lambda name, text: calls.append((name, text)),
        )
        time.sleep(0.12)

        self.assertEqual(rspecs, {})
        self.assertIn("timed out", errors["Slow"])
        self.assertEqual(calls, [])


if __name__ == "__main__":
    unittest.main()
