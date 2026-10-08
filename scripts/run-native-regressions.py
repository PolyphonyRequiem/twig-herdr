#!/usr/bin/env python3
"""Run selected, already-built Release regressions using the pinned native CI runner.

Usage: python scripts/run-native-regressions.py --source native-source
"""

import argparse
from collections import Counter
import importlib.util
import json
from pathlib import Path
import sys
import xml.etree.ElementTree as ET


SELECTIONS = {
    "Cli": (
        "WorkspaceCommandTests", "BenchCommandTests", "BenchConfigurationCommandTests",
        "BenchSyncCommandTests", "TrackingCommandTests", "ConnectionBindingReadTests",
        "PatConnectionBindingReadTests", "ConnectionBindingTransitionConsumerTests",
        "BenchDetailCommandTests", "BenchAreaCandidatesCommandTests", "RichHtmlRendererTests",
        "FormatterHelpersTests", "ProgressiveHelpTests",
    ),
    "Infrastructure": ("Bench", "Connection"),
    "Domain": ("Bench", "WorkingSetFollowsCurrentBenchTests"),
}


def load_native(source):
    path = source.resolve() / "tools" / "run-ci-tests.py"
    spec = importlib.util.spec_from_file_location("twig_native_ci_runner", path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"cannot import native CI runner: {path}")
    native = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = native  # dataclasses resolve their module during import.
    spec.loader.exec_module(native)
    return native


def run(native):
    runner = native.Runner()
    framework = ET.parse(native.ROOT / "Directory.Build.props").getroot().findtext("PropertyGroup/TargetFramework")
    native.require(bool(framework), "test target framework missing")
    failed = False
    totals = Counter()
    for assembly, selectors in SELECTIONS.items():
        try:
            dll = native.ROOT / "tests" / f"Twig.{assembly}.Tests" / "bin" / "Release" / framework / f"Twig.{assembly}.Tests.dll"
            native.require(dll.is_file(), f"built Release assembly missing: {dll}")
            selection = "|".join(f"FullyQualifiedName~{selector}" for selector in selectors)
            filter_expression = f"({runner.base_filter})&({selection})"
            output, exit_code = runner.invoke(dll, ["/ListTests"], f"{assembly}-discovery")
            native.check_process(output, exit_code)
            all_cases = native.display_cases(output, assembly)
            methods = runner.discover_methods(dll, filter_expression, f"{assembly}-eligible")
            cases = native.eligible_cases(all_cases, methods)
            if assembly == "Cli":
                host_methods = runner.discover_methods(dll, f"({filter_expression})&Category=HostInventory", "Cli-host-discovery")
                original_host_classes = native.HOST_CLASSES
                selected_classes = {method.rsplit(".", 1)[0] for method in methods}
                host_classes = original_host_classes & selected_classes
                expected_host = {method for method in methods if method.rsplit(".", 1)[0] in host_classes}
                native.require(host_methods == expected_host,
                               f"selected HostInventory trait mismatch: missing={sorted(expected_host - host_methods)}, extra={sorted(host_methods - expected_host)}")
                native.require({method.rsplit(".", 1)[0] for method in host_methods} == host_classes,
                               "a selected native inventory class is missing")
                # Adapt only the known class inventory, after checking every selected host trait.
                native.HOST_CLASSES = host_classes
                try:
                    partitions = native.partition_cli(cases, host_methods, filter_expression)
                finally:
                    native.HOST_CLASSES = original_host_classes
            else:
                partitions = [native.Partition(assembly, filter_expression, cases)]
            manifest = [{"name": part.name, "filter": part.filter, "expected": sum(part.cases.values()),
                         "hostInventory": part.host_inventory, "cases": dict(sorted(part.cases.items()))} for part in partitions]
            (runner.logs / f"{assembly}-plan.json").write_text(json.dumps(manifest, indent=2), encoding="utf-8")
            print(f"TWIG-DISCOVERY {assembly}: {sum(cases.values())} eligible selected rows, {len(partitions)} sessions")
            completed = []
            for part in partitions:
                try:
                    results = runner.logs / part.name
                    results.mkdir()
                    output, exit_code = runner.invoke(dll, [f"/TestCaseFilter:{part.filter}", f"/ResultsDirectory:{results}",
                                                           "/Logger:trx;LogFileName=results.trx"], part.name, part.host_inventory)
                    actual, passed, skipped = native.reconcile_result(output, exit_code, results / "results.trx", part.cases)
                    completed.append(actual)
                    totals.update(passed=passed, skipped=skipped)
                    print(f"TWIG-VERDICT {part.name}: PASSED ({passed} passed, {skipped} skipped; {sum(part.cases.values())} expected)")
                except (native.RunFailure, OSError, ET.ParseError, ValueError) as error:
                    failed = True
                    print(f"TWIG-VERDICT {part.name}: FAILED ({error})")
            native.reconcile_coverage(cases, completed)
            print(f"TWIG-COVERAGE {assembly}: {sum(cases.values())}/{sum(cases.values())} exactly once")
        except (native.RunFailure, OSError, ET.ParseError, ValueError) as error:
            failed = True
            print(f"TWIG-VERDICT {assembly}: FAILED ({error})")
    print(f"TWIG-VERDICT OVERALL: {'FAILED' if failed else 'PASSED'} ({totals['passed']} passed, {totals['skipped']} skipped) [logs: {runner.logs}]")
    return int(failed)


def main():
    sys.stdout.reconfigure(encoding="utf-8")
    sys.stderr.reconfigure(encoding="utf-8")
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", required=True, type=Path, help="pinned native checkout with already-built Release test projects")
    args = parser.parse_args()
    try:
        return run(load_native(args.source))
    except (RuntimeError, OSError, ET.ParseError, ValueError, ImportError) as error:
        print(f"TWIG-VERDICT OVERALL: FAILED ({error})")
        return 1


if __name__ == "__main__":
    sys.exit(main())
