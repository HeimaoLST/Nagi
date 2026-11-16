import AppKit
import ApplicationServices
import Foundation

guard CommandLine.arguments.count == 5,
      CommandLine.arguments[1] == "--output", CommandLine.arguments[3] == "--phase",
      ["fresh", "busy", "edit"].contains(CommandLine.arguments[4]) else {
    fatalError("usage: ax-probe --output ABSOLUTE_CASE_DIRECTORY --phase fresh|busy|edit")
}
let root = URL(fileURLWithPath: CommandLine.arguments[2]).resolvingSymlinksInPath()
let phase = CommandLine.arguments[4]
let caseData = try Data(contentsOf: root.appendingPathComponent("case.json"))
let caseInfo = try JSONSerialization.jsonObject(with: caseData) as! [String: Any]
let sceneData = try Data(contentsOf: root.appendingPathComponent("scene.json"))
let scene = try JSONSerialization.jsonObject(with: sceneData) as! [String: Any]
let childData = try Data(contentsOf: root.appendingPathComponent("logs/native.command.json"))
let child = try JSONSerialization.jsonObject(with: childData) as! [String: Any]
let pid = (scene["pid"] as! NSNumber).int32Value
let expectedExecutable = caseInfo["executable"] as! String
let expectedBundle = caseInfo["bundle_id"] as! String
let output = root.appendingPathComponent("ax-\(phase).json")
guard !FileManager.default.fileExists(atPath: output.path) else {
    fatalError("observation already exists; preserve it")
}
var report: [String: Any] = [
    "pid": pid, "phase": phase, "case_id": caseInfo["case_id"] as! String,
    "executable": expectedExecutable, "bundle_id": expectedBundle,
    "all_queried": false, "all_reads_succeeded": false, "no_exposure": false,
    "secure_subrole": false, "enabled_known": false, "enabled": false,
    "queries": [], "status": "UNVERIFIED",
    "scope": "read-only exact owned fixture; no permission request or AX action"
]

func finish(_ code: Int32, _ reason: String) -> Never {
    report["reason"] = reason
    do {
        try JSONSerialization.data(withJSONObject: report, options: [.prettyPrinted, .sortedKeys]).write(to: output, options: .atomic)
    } catch {
        fputs("cannot write owned AX observation: \(error)\n", stderr)
        exit(2)
    }
    print(output.path)
    exit(code)
}

let app = NSRunningApplication(processIdentifier: pid)
guard caseInfo["output"] as? String == root.path,
      (child["pid"] as? NSNumber)?.int32Value == pid,
      child["finished_at"] == nil,
      app?.executableURL?.resolvingSymlinksInPath().path == URL(fileURLWithPath: expectedExecutable).resolvingSymlinksInPath().path,
      app?.bundleIdentifier == expectedBundle,
      scene["phase"] as? String == phase, scene["draft_exact"] as? Bool == true else {
    finish(2, "owned process, case, phase or exact synthetic draft does not match")
}
report["probe_ax_trusted"] = AXIsProcessTrusted()
guard AXIsProcessTrusted() else {
    report["status"] = "probe_permission_denied"
    finish(2, "this read-only probe is not AX trusted; no prompt requested; CUA availability is not inferred")
}

func attribute(_ node: AXUIElement, _ name: String) -> (AXError, CFTypeRef?) {
    var value: CFTypeRef?
    let code = AXUIElementCopyAttributeValue(node, name as CFString, &value)
    return (code, value)
}

func stringAttribute(_ node: AXUIElement, _ name: String) -> String {
    return attribute(node, name).1 as? String ?? ""
}

let application = AXUIElementCreateApplication(pid)
var stack = [application]
var visited = [AXUIElement]()
var fields = [AXUIElement]()
var lookupErrors = [Int32]()
while let node = stack.popLast() {
    if visited.contains(where: { CFEqual($0, node) }) { continue }
    visited.append(node)
    guard visited.count <= 3000 else { finish(2, "owned AX tree exceeded bounded traversal") }
    if stringAttribute(node, "AXRole") == "AXTextField" &&
       (stringAttribute(node, "AXTitle") == "API key" || stringAttribute(node, "AXDescription") == "API key") {
        fields.append(node)
    }
    let (code, children) = attribute(node, "AXChildren")
    if code == .success, let children = children as? [AXUIElement] {
        stack.append(contentsOf: children)
    } else if code != .success && code != .attributeUnsupported && code != .noValue {
        lookupErrors.append(code.rawValue)
    }
}
report["lookup_ax_errors"] = lookupErrors
report["matching_fields"] = fields.count
guard fields.count == 1 else { finish(2, "actual API key field is not uniquely observable") }
let field = fields[0]
let sentinel = "Nagi_SYNTHETIC_AX_20261116_Aa7"
var queries = [[String: Any]]()
var allSucceeded = true
var emptyReads = true
var sentinelObserved = false

func observation(_ name: String, _ code: AXError, _ value: CFTypeRef?) -> [String: Any] {
    var result: [String: Any] = ["attribute": name, "ax_error": code.rawValue]
    guard code == .success, let value = value else {
        allSucceeded = false
        result["returned_value"] = false
        if code == .attributeUnsupported || code == .parameterizedAttributeUnsupported {
            result["status"] = "unsupported"
        } else if code == .noValue || code == .success {
            result["status"] = "no_value"
        } else if code == .apiDisabled {
            result["status"] = "api_disabled"
        } else {
            result["status"] = "read_failed"
        }
        result["verification"] = "UNVERIFIED"
        return result
    }
    result["returned_value"] = true
    result["status"] = "read"
    if let text = value as? String {
        result["utf16_count"] = text.utf16.count
        result["contains_sentinel"] = text.contains(sentinel)
        sentinelObserved = sentinelObserved || text.contains(sentinel)
        emptyReads = emptyReads && text.isEmpty
    } else if let text = value as? NSAttributedString {
        result["utf16_count"] = text.string.utf16.count
        result["contains_sentinel"] = text.string.contains(sentinel)
        sentinelObserved = sentinelObserved || text.string.contains(sentinel)
        emptyReads = emptyReads && text.string.isEmpty
    } else if let number = value as? NSNumber {
        result["number"] = number.intValue
        emptyReads = emptyReads && number.intValue == 0
    } else if CFGetTypeID(value) == AXValueGetTypeID() {
        let rangeValue = value as! AXValue
        var range = CFRange(location: 0, length: 0)
        let decoded = AXValueGetValue(rangeValue, .cfRange, &range)
        result["range_decoded"] = decoded
        result["range_location"] = range.location
        result["range_length"] = range.length
        allSucceeded = allSucceeded && decoded
        emptyReads = emptyReads && decoded && range.location == 0 && range.length == 0
    } else {
        result["status"] = "unexpected_type"
        result["verification"] = "UNVERIFIED"
        allSucceeded = false
    }
    return result
}

for name in ["AXValue", "AXSelectedText", "AXSelectedTextRange", "AXNumberOfCharacters"] {
    let (code, value) = attribute(field, name)
    queries.append(observation(name, code, value))
}
var range = CFRange(location: 0, length: sentinel.utf16.count)
guard let rangeValue = AXValueCreate(.cfRange, &range) else { finish(2, "cannot create owned synthetic range") }
for name in ["AXStringForRange", "AXAttributedStringForRange"] {
    var value: CFTypeRef?
    let code = AXUIElementCopyParameterizedAttributeValue(field, name as CFString, rangeValue, &value)
    queries.append(observation(name, code, value))
}
let (enabledCode, enabledValue) = attribute(field, "AXEnabled")
let enabled = enabledValue as? Bool
let (subroleCode, subroleValue) = attribute(field, "AXSubrole")
let secure = subroleCode == .success && subroleValue as? String == "AXSecureTextField"
var settable = DarwinBoolean(false)
let settableCode = AXUIElementIsAttributeSettable(field, "AXValue" as CFString, &settable)
report["queries"] = queries
report["all_queried"] = queries.count == 6
report["all_reads_succeeded"] = allSucceeded && queries.count == 6
report["no_exposure"] = allSucceeded && emptyReads && queries.count == 6
report["sentinel_observed"] = sentinelObserved
report["secure_subrole"] = secure
report["subrole_ax_error"] = subroleCode.rawValue
report["enabled_known"] = enabledCode == .success && enabled != nil
report["enabled"] = enabled ?? false
report["enabled_ax_error"] = enabledCode.rawValue
report["value_settable"] = settable.boolValue
report["value_settable_ax_error"] = settableCode.rawValue
if !allSucceeded || enabledCode != .success || enabled == nil || subroleCode != .success {
    finish(2, "some AX reads are unavailable; absence of data is not successful no-exposure evidence")
}
if !emptyReads || !secure || enabled != (phase != "busy") {
    report["status"] = sentinelObserved ? "observed_sentinel_exposure" : "observed_control_contract_failure"
    finish(1, "observed secure-value, subrole or enabled-state contract failure; inspect exact fields")
}
report["status"] = "observed_no_exposure"
finish(0, "six actual successful reads were empty/zero in this exact phase; not full product or real-key readiness")
