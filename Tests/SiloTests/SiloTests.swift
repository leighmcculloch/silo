import XCTest
@testable import Config
@testable import CLI

final class ConfigTests: XCTestCase {
    func testDefaultConfig() {
        let cfg = ConfigManager.defaultConfig()

        // Check that default tools are defined
        XCTAssertNotNil(cfg.tools["claude"])
        XCTAssertNotNil(cfg.tools["opencode"])
        XCTAssertNotNil(cfg.tools["copilot"])

        // Check claude has expected mounts
        XCTAssertTrue(cfg.tools["claude"]?.mountsRW.contains("~/.claude.json") ?? false)
        XCTAssertTrue(cfg.tools["claude"]?.mountsRW.contains("~/.claude") ?? false)

        // Check copilot has expected env var
        XCTAssertTrue(cfg.tools["copilot"]?.env.contains("COPILOT_GITHUB_TOKEN") ?? false)
    }

    func testConfigMerge() {
        let base = Config(
            mountsRO: ["/base/ro"],
            mountsRW: ["/base/rw"],
            env: ["BASE_VAR"],
            prehooks: ["echo base"],
            tools: [
                "claude": ToolConfig(mountsRW: ["~/.claude"])
            ]
        )

        let overlay = Config(
            mountsRO: ["/overlay/ro"],
            mountsRW: ["/overlay/rw"],
            env: ["OVERLAY_VAR"],
            prehooks: ["echo overlay"],
            tools: [
                "claude": ToolConfig(env: ["CLAUDE_KEY"]),
                "opencode": ToolConfig(mountsRW: ["~/.opencode"])
            ]
        )

        let merged = ConfigManager.merge(base: base, overlay: overlay)

        // Arrays should be appended
        XCTAssertEqual(merged.mountsRO, ["/base/ro", "/overlay/ro"])
        XCTAssertEqual(merged.mountsRW, ["/base/rw", "/overlay/rw"])
        XCTAssertEqual(merged.env, ["BASE_VAR", "OVERLAY_VAR"])
        XCTAssertEqual(merged.prehooks, ["echo base", "echo overlay"])

        // Tool configs should be merged
        XCTAssertEqual(merged.tools["claude"]?.mountsRW, ["~/.claude"])
        XCTAssertEqual(merged.tools["claude"]?.env, ["CLAUDE_KEY"])
        XCTAssertEqual(merged.tools["opencode"]?.mountsRW, ["~/.opencode"])
    }

    func testXDGConfigHome() {
        let configHome = ConfigManager.xdgConfigHome()
        XCTAssertFalse(configHome.isEmpty)
    }

    func testGetConfigPaths() {
        let paths = ConfigManager.getConfigPaths()
        XCTAssertGreaterThan(paths.count, 0)

        // First path should be global config
        XCTAssertTrue(paths[0].path.contains("silo/silo.jsonc"))
    }
}

final class CLITests: XCTestCase {
    func testStyled() {
        let styledText = styled("test", .cyan)
        XCTAssertTrue(styledText.contains("test"))
        XCTAssertTrue(styledText.contains("\u{001B}"))
    }

    func testBold() {
        let boldText = bold("test")
        XCTAssertTrue(boldText.contains("test"))
        XCTAssertTrue(boldText.contains("\u{001B}"))
    }
}
