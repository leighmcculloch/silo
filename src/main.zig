const std = @import("std");
const posix = std.posix;
const fs = std.fs;
const mem = std.mem;
const json = std.json;
const Allocator = std.mem.Allocator;

// Version embedded at compile time
pub const version = "dev";

// Embedded Dockerfile content
pub const dockerfile_content = @embedFile("Dockerfile");

// ============================================
// CLI Output Styling Module
// ============================================

pub const cli = struct {
    // ANSI color codes
    const ESC = "\x1b[";
    const RESET = ESC ++ "0m";

    const Color = enum(u8) {
        cyan = 86,
        green = 82,
        yellow = 214,
        red = 196,
        magenta = 205,
        dim = 241,
    };

    fn colorCode(c: Color) []const u8 {
        return switch (c) {
            .cyan => ESC ++ "38;5;86m",
            .green => ESC ++ "38;5;82m",
            .yellow => ESC ++ "38;5;214m",
            .red => ESC ++ "38;5;196m",
            .magenta => ESC ++ "38;5;205m",
            .dim => ESC ++ "38;5;241m",
        };
    }

    pub fn log(writer: anytype, comptime format: []const u8, args: anytype) void {
        const msg = std.fmt.allocPrint(std.heap.page_allocator, format, args) catch return;
        defer std.heap.page_allocator.free(msg);
        writer.print("{s}==> {s}{s}\n", .{ colorCode(.cyan), msg, RESET }) catch {};
    }

    pub fn logSuccess(writer: anytype, comptime format: []const u8, args: anytype) void {
        const msg = std.fmt.allocPrint(std.heap.page_allocator, format, args) catch return;
        defer std.heap.page_allocator.free(msg);
        writer.print("{s}✓ {s}{s}\n", .{ colorCode(.green), msg, RESET }) catch {};
    }

    pub fn logWarning(writer: anytype, comptime format: []const u8, args: anytype) void {
        const msg = std.fmt.allocPrint(std.heap.page_allocator, format, args) catch return;
        defer std.heap.page_allocator.free(msg);
        writer.print("{s}! {s}{s}\n", .{ colorCode(.yellow), msg, RESET }) catch {};
    }

    pub fn logError(writer: anytype, comptime format: []const u8, args: anytype) void {
        const msg = std.fmt.allocPrint(std.heap.page_allocator, format, args) catch return;
        defer std.heap.page_allocator.free(msg);
        writer.print("{s}✗ {s}{s}\n", .{ colorCode(.red), msg, RESET }) catch {};
    }

    pub fn logBullet(writer: anytype, comptime format: []const u8, args: anytype) void {
        const msg = std.fmt.allocPrint(std.heap.page_allocator, format, args) catch return;
        defer std.heap.page_allocator.free(msg);
        writer.print("  {s}→{s} {s}\n", .{ colorCode(.magenta), RESET, msg }) catch {};
    }

    pub fn logDim(writer: anytype, comptime format: []const u8, args: anytype) void {
        const msg = std.fmt.allocPrint(std.heap.page_allocator, format, args) catch return;
        defer std.heap.page_allocator.free(msg);
        writer.print("{s}  {s}{s}\n", .{ colorCode(.dim), msg, RESET }) catch {};
    }

    pub fn renderMagenta(text: []const u8) ![]const u8 {
        return std.fmt.allocPrint(std.heap.page_allocator, "{s}{s}{s}", .{ colorCode(.magenta), text, RESET });
    }
};

// ============================================
// Configuration Module
// ============================================

pub const config = struct {
    pub const ToolConfig = struct {
        mounts_ro: []const []const u8 = &.{},
        mounts_rw: []const []const u8 = &.{},
        env: []const []const u8 = &.{},

        pub fn deinit(self: *ToolConfig, allocator: Allocator) void {
            for (self.mounts_ro) |s| allocator.free(s);
            if (self.mounts_ro.len > 0) allocator.free(self.mounts_ro);
            for (self.mounts_rw) |s| allocator.free(s);
            if (self.mounts_rw.len > 0) allocator.free(self.mounts_rw);
            for (self.env) |s| allocator.free(s);
            if (self.env.len > 0) allocator.free(self.env);
        }
    };

    pub const Config = struct {
        mounts_ro: std.ArrayList([]const u8),
        mounts_rw: std.ArrayList([]const u8),
        env: std.ArrayList([]const u8),
        prehooks: std.ArrayList([]const u8),
        tools: std.StringHashMap(ToolConfig),
        allocator: Allocator,

        pub fn init(allocator: Allocator) Config {
            return .{
                .mounts_ro = std.ArrayList([]const u8).init(allocator),
                .mounts_rw = std.ArrayList([]const u8).init(allocator),
                .env = std.ArrayList([]const u8).init(allocator),
                .prehooks = std.ArrayList([]const u8).init(allocator),
                .tools = std.StringHashMap(ToolConfig).init(allocator),
                .allocator = allocator,
            };
        }

        pub fn deinit(self: *Config) void {
            for (self.mounts_ro.items) |s| self.allocator.free(s);
            self.mounts_ro.deinit();
            for (self.mounts_rw.items) |s| self.allocator.free(s);
            self.mounts_rw.deinit();
            for (self.env.items) |s| self.allocator.free(s);
            self.env.deinit();
            for (self.prehooks.items) |s| self.allocator.free(s);
            self.prehooks.deinit();

            var it = self.tools.iterator();
            while (it.next()) |entry| {
                self.allocator.free(entry.key_ptr.*);
                var tool_cfg = entry.value_ptr.*;
                tool_cfg.deinit(self.allocator);
            }
            self.tools.deinit();
        }

        pub fn getToolConfig(self: *const Config, tool: []const u8) ?ToolConfig {
            return self.tools.get(tool);
        }
    };

    pub const ConfigPath = struct {
        path: []const u8,
        exists: bool,
    };

    /// Get XDG config home directory
    pub fn getXDGConfigHome(allocator: Allocator) ![]const u8 {
        if (std.posix.getenv("XDG_CONFIG_HOME")) |xdg| {
            return try allocator.dupe(u8, xdg);
        }
        const home = std.posix.getenv("HOME") orelse return error.NoHome;
        return try std.fmt.allocPrint(allocator, "{s}/.config", .{home});
    }

    /// Get XDG data home directory
    pub fn getXDGDataHome(allocator: Allocator) ![]const u8 {
        if (std.posix.getenv("XDG_DATA_HOME")) |xdg| {
            return try allocator.dupe(u8, xdg);
        }
        const home = std.posix.getenv("HOME") orelse return error.NoHome;
        return try std.fmt.allocPrint(allocator, "{s}/.local/share", .{home});
    }

    /// Expand ~ to home directory
    pub fn expandPath(allocator: Allocator, path: []const u8) ![]const u8 {
        if (mem.startsWith(u8, path, "~/")) {
            const home = std.posix.getenv("HOME") orelse return error.NoHome;
            return try std.fmt.allocPrint(allocator, "{s}{s}", .{ home, path[1..] });
        }
        if (mem.eql(u8, path, "~")) {
            const home = std.posix.getenv("HOME") orelse return error.NoHome;
            return try allocator.dupe(u8, home);
        }
        return try allocator.dupe(u8, path);
    }

    /// Create default configuration
    pub fn defaultConfig(allocator: Allocator) !Config {
        var cfg = Config.init(allocator);

        const xdg_config = try getXDGConfigHome(allocator);
        defer allocator.free(xdg_config);
        const xdg_data = try getXDGDataHome(allocator);
        defer allocator.free(xdg_data);

        // Claude tool config
        var claude_cfg = ToolConfig{};
        var claude_mounts = std.ArrayList([]const u8).init(allocator);
        try claude_mounts.append(try allocator.dupe(u8, "~/.claude.json"));
        try claude_mounts.append(try allocator.dupe(u8, "~/.claude"));
        claude_cfg.mounts_rw = try claude_mounts.toOwnedSlice();
        try cfg.tools.put(try allocator.dupe(u8, "claude"), claude_cfg);

        // OpenCode tool config
        var opencode_cfg = ToolConfig{};
        var opencode_mounts = std.ArrayList([]const u8).init(allocator);
        const opencode_config_path = try std.fmt.allocPrint(allocator, "{s}/opencode", .{xdg_config});
        const opencode_data_path = try std.fmt.allocPrint(allocator, "{s}/opencode", .{xdg_data});
        try opencode_mounts.append(try tildePath(allocator, opencode_config_path));
        allocator.free(opencode_config_path);
        try opencode_mounts.append(try tildePath(allocator, opencode_data_path));
        allocator.free(opencode_data_path);
        opencode_cfg.mounts_rw = try opencode_mounts.toOwnedSlice();
        try cfg.tools.put(try allocator.dupe(u8, "opencode"), opencode_cfg);

        // Copilot tool config
        var copilot_cfg = ToolConfig{};
        var copilot_mounts = std.ArrayList([]const u8).init(allocator);
        const copilot_config_path = try std.fmt.allocPrint(allocator, "{s}/.copilot", .{xdg_config});
        try copilot_mounts.append(try tildePath(allocator, copilot_config_path));
        allocator.free(copilot_config_path);
        copilot_cfg.mounts_rw = try copilot_mounts.toOwnedSlice();
        var copilot_env = std.ArrayList([]const u8).init(allocator);
        try copilot_env.append(try allocator.dupe(u8, "COPILOT_GITHUB_TOKEN"));
        copilot_cfg.env = try copilot_env.toOwnedSlice();
        try cfg.tools.put(try allocator.dupe(u8, "copilot"), copilot_cfg);

        return cfg;
    }

    /// Replace home dir with ~ in path
    fn tildePath(allocator: Allocator, path: []const u8) ![]const u8 {
        const home = std.posix.getenv("HOME") orelse return try allocator.dupe(u8, path);
        if (mem.startsWith(u8, path, home)) {
            return try std.fmt.allocPrint(allocator, "~{s}", .{path[home.len..]});
        }
        return try allocator.dupe(u8, path);
    }

    /// Strip JSONC comments from content
    pub fn stripJsonComments(allocator: Allocator, content: []const u8) ![]const u8 {
        var result = std.ArrayList(u8).init(allocator);
        var i: usize = 0;
        var in_string = false;
        var escape_next = false;

        while (i < content.len) {
            if (escape_next) {
                try result.append(content[i]);
                escape_next = false;
                i += 1;
                continue;
            }

            if (content[i] == '\\' and in_string) {
                try result.append(content[i]);
                escape_next = true;
                i += 1;
                continue;
            }

            if (content[i] == '"' and !escape_next) {
                in_string = !in_string;
                try result.append(content[i]);
                i += 1;
                continue;
            }

            if (!in_string and i + 1 < content.len) {
                // Line comment
                if (content[i] == '/' and content[i + 1] == '/') {
                    while (i < content.len and content[i] != '\n') {
                        i += 1;
                    }
                    continue;
                }
                // Block comment
                if (content[i] == '/' and content[i + 1] == '*') {
                    i += 2;
                    while (i + 1 < content.len and !(content[i] == '*' and content[i + 1] == '/')) {
                        i += 1;
                    }
                    i += 2;
                    continue;
                }
            }

            try result.append(content[i]);
            i += 1;
        }

        return try result.toOwnedSlice();
    }

    /// Load configuration from a file path
    pub fn load(allocator: Allocator, path: []const u8) !Config {
        const file = try fs.openFileAbsolute(path, .{});
        defer file.close();

        const content = try file.readToEndAlloc(allocator, 1024 * 1024);
        defer allocator.free(content);

        const json_content = try stripJsonComments(allocator, content);
        defer allocator.free(json_content);

        return try parseConfig(allocator, json_content);
    }

    /// Parse JSON config content into Config struct
    fn parseConfig(allocator: Allocator, json_content: []const u8) !Config {
        var cfg = Config.init(allocator);
        errdefer cfg.deinit();

        const parsed = json.parseFromSlice(json.Value, allocator, json_content, .{}) catch |err| {
            std.debug.print("JSON parse error: {}\n", .{err});
            return cfg;
        };
        defer parsed.deinit();

        const root = parsed.value;
        if (root != .object) return cfg;

        // Parse mounts_ro
        if (root.object.get("mounts_ro")) |arr| {
            if (arr == .array) {
                for (arr.array.items) |item| {
                    if (item == .string) {
                        try cfg.mounts_ro.append(try allocator.dupe(u8, item.string));
                    }
                }
            }
        }

        // Parse mounts_rw
        if (root.object.get("mounts_rw")) |arr| {
            if (arr == .array) {
                for (arr.array.items) |item| {
                    if (item == .string) {
                        try cfg.mounts_rw.append(try allocator.dupe(u8, item.string));
                    }
                }
            }
        }

        // Parse env
        if (root.object.get("env")) |arr| {
            if (arr == .array) {
                for (arr.array.items) |item| {
                    if (item == .string) {
                        try cfg.env.append(try allocator.dupe(u8, item.string));
                    }
                }
            }
        }

        // Parse prehooks
        if (root.object.get("prehooks")) |arr| {
            if (arr == .array) {
                for (arr.array.items) |item| {
                    if (item == .string) {
                        try cfg.prehooks.append(try allocator.dupe(u8, item.string));
                    }
                }
            }
        }

        // Parse tools
        if (root.object.get("tools")) |tools_obj| {
            if (tools_obj == .object) {
                var it = tools_obj.object.iterator();
                while (it.next()) |entry| {
                    const tool_name = entry.key_ptr.*;
                    const tool_val = entry.value_ptr.*;

                    if (tool_val == .object) {
                        var tool_cfg = ToolConfig{};

                        if (tool_val.object.get("mounts_ro")) |arr| {
                            if (arr == .array) {
                                var mounts = std.ArrayList([]const u8).init(allocator);
                                for (arr.array.items) |item| {
                                    if (item == .string) {
                                        try mounts.append(try allocator.dupe(u8, item.string));
                                    }
                                }
                                tool_cfg.mounts_ro = try mounts.toOwnedSlice();
                            }
                        }

                        if (tool_val.object.get("mounts_rw")) |arr| {
                            if (arr == .array) {
                                var mounts = std.ArrayList([]const u8).init(allocator);
                                for (arr.array.items) |item| {
                                    if (item == .string) {
                                        try mounts.append(try allocator.dupe(u8, item.string));
                                    }
                                }
                                tool_cfg.mounts_rw = try mounts.toOwnedSlice();
                            }
                        }

                        if (tool_val.object.get("env")) |arr| {
                            if (arr == .array) {
                                var envs = std.ArrayList([]const u8).init(allocator);
                                for (arr.array.items) |item| {
                                    if (item == .string) {
                                        try envs.append(try allocator.dupe(u8, item.string));
                                    }
                                }
                                tool_cfg.env = try envs.toOwnedSlice();
                            }
                        }

                        try cfg.tools.put(try allocator.dupe(u8, tool_name), tool_cfg);
                    }
                }
            }
        }

        return cfg;
    }

    /// Merge two configs (overlay takes precedence)
    pub fn merge(allocator: Allocator, base: *Config, overlay: *const Config) !void {
        // Append arrays
        for (overlay.mounts_ro.items) |item| {
            try base.mounts_ro.append(try allocator.dupe(u8, item));
        }
        for (overlay.mounts_rw.items) |item| {
            try base.mounts_rw.append(try allocator.dupe(u8, item));
        }
        for (overlay.env.items) |item| {
            try base.env.append(try allocator.dupe(u8, item));
        }
        for (overlay.prehooks.items) |item| {
            try base.prehooks.append(try allocator.dupe(u8, item));
        }

        // Merge tools
        var it = overlay.tools.iterator();
        while (it.next()) |entry| {
            const name = entry.key_ptr.*;
            const tool = entry.value_ptr.*;

            if (base.tools.getPtr(name)) |existing| {
                // Merge existing tool config
                var new_mounts_ro = std.ArrayList([]const u8).init(allocator);
                for (existing.mounts_ro) |m| try new_mounts_ro.append(try allocator.dupe(u8, m));
                for (tool.mounts_ro) |m| try new_mounts_ro.append(try allocator.dupe(u8, m));

                var new_mounts_rw = std.ArrayList([]const u8).init(allocator);
                for (existing.mounts_rw) |m| try new_mounts_rw.append(try allocator.dupe(u8, m));
                for (tool.mounts_rw) |m| try new_mounts_rw.append(try allocator.dupe(u8, m));

                var new_env = std.ArrayList([]const u8).init(allocator);
                for (existing.env) |e| try new_env.append(try allocator.dupe(u8, e));
                for (tool.env) |e| try new_env.append(try allocator.dupe(u8, e));

                // Free old slices
                for (existing.mounts_ro) |s| allocator.free(s);
                if (existing.mounts_ro.len > 0) allocator.free(existing.mounts_ro);
                for (existing.mounts_rw) |s| allocator.free(s);
                if (existing.mounts_rw.len > 0) allocator.free(existing.mounts_rw);
                for (existing.env) |s| allocator.free(s);
                if (existing.env.len > 0) allocator.free(existing.env);

                existing.mounts_ro = try new_mounts_ro.toOwnedSlice();
                existing.mounts_rw = try new_mounts_rw.toOwnedSlice();
                existing.env = try new_env.toOwnedSlice();
            } else {
                // Add new tool config
                var new_tool = ToolConfig{};

                var new_mounts_ro = std.ArrayList([]const u8).init(allocator);
                for (tool.mounts_ro) |m| try new_mounts_ro.append(try allocator.dupe(u8, m));
                new_tool.mounts_ro = try new_mounts_ro.toOwnedSlice();

                var new_mounts_rw = std.ArrayList([]const u8).init(allocator);
                for (tool.mounts_rw) |m| try new_mounts_rw.append(try allocator.dupe(u8, m));
                new_tool.mounts_rw = try new_mounts_rw.toOwnedSlice();

                var new_env = std.ArrayList([]const u8).init(allocator);
                for (tool.env) |e| try new_env.append(try allocator.dupe(u8, e));
                new_tool.env = try new_env.toOwnedSlice();

                try base.tools.put(try allocator.dupe(u8, name), new_tool);
            }
        }
    }

    /// Get all config paths
    pub fn getConfigPaths(allocator: Allocator) ![]ConfigPath {
        var paths = std.ArrayList(ConfigPath).init(allocator);

        // Global config
        const xdg_config = try getXDGConfigHome(allocator);
        defer allocator.free(xdg_config);
        const global_path = try std.fmt.allocPrint(allocator, "{s}/silo/silo.jsonc", .{xdg_config});
        const global_exists = blk: {
            fs.accessAbsolute(global_path, .{}) catch break :blk false;
            break :blk true;
        };
        try paths.append(.{ .path = global_path, .exists = global_exists });

        // Local configs from root to cwd
        var cwd_buf: [fs.max_path_bytes]u8 = undefined;
        const cwd = try std.posix.getcwd(&cwd_buf);

        var local_paths = std.ArrayList(ConfigPath).init(allocator);
        defer local_paths.deinit();

        var dir: []const u8 = cwd;
        while (true) {
            const config_path = try std.fmt.allocPrint(allocator, "{s}/silo.jsonc", .{dir});
            const exists = blk: {
                fs.accessAbsolute(config_path, .{}) catch break :blk false;
                break :blk true;
            };
            try local_paths.insert(0, .{ .path = config_path, .exists = exists });

            if (mem.eql(u8, dir, "/")) break;
            dir = fs.path.dirname(dir) orelse break;
        }

        for (local_paths.items) |p| {
            try paths.append(p);
        }

        return try paths.toOwnedSlice();
    }

    /// Load all configs and merge them
    pub fn loadAll(allocator: Allocator) !Config {
        var cfg = try defaultConfig(allocator);

        // Load global config
        const xdg_config = try getXDGConfigHome(allocator);
        defer allocator.free(xdg_config);
        const global_path = try std.fmt.allocPrint(allocator, "{s}/silo/silo.jsonc", .{xdg_config});
        defer allocator.free(global_path);

        if (load(allocator, global_path)) |global_cfg| {
            var overlay = global_cfg;
            try merge(allocator, &cfg, &overlay);
            overlay.deinit();
        } else |_| {}

        // Load local configs
        var cwd_buf: [fs.max_path_bytes]u8 = undefined;
        const cwd = try std.posix.getcwd(&cwd_buf);

        var config_paths = std.ArrayList([]const u8).init(allocator);
        defer {
            for (config_paths.items) |p| allocator.free(p);
            config_paths.deinit();
        }

        var dir: []const u8 = cwd;
        while (true) {
            const config_path = try std.fmt.allocPrint(allocator, "{s}/silo.jsonc", .{dir});
            if (fs.accessAbsolute(config_path, .{})) |_| {
                try config_paths.insert(0, config_path);
            } else |_| {
                allocator.free(config_path);
            }

            if (mem.eql(u8, dir, "/")) break;
            dir = fs.path.dirname(dir) orelse break;
        }

        for (config_paths.items) |path| {
            if (load(allocator, path)) |local_cfg| {
                var overlay = local_cfg;
                try merge(allocator, &cfg, &overlay);
                overlay.deinit();
            } else |_| {}
        }

        return cfg;
    }
};

// ============================================
// Docker Module
// ============================================

pub const docker = struct {
    /// Execute a docker command and return output
    fn execDocker(allocator: Allocator, args: []const []const u8) ![]const u8 {
        var argv = std.ArrayList([]const u8).init(allocator);
        defer argv.deinit();

        try argv.append("docker");
        for (args) |arg| {
            try argv.append(arg);
        }

        var child = std.process.Child.init(argv.items, allocator);
        child.stdout_behavior = .Pipe;
        child.stderr_behavior = .Pipe;

        try child.spawn();

        const stdout = try child.stdout.?.reader().readAllAlloc(allocator, 1024 * 1024);
        _ = child.wait() catch {};

        return stdout;
    }

    /// Build Docker image
    pub fn build(allocator: Allocator, dockerfile: []const u8, target: []const u8, build_args: std.StringHashMap([]const u8), stderr: anytype) !void {
        // Create a temporary directory for the Dockerfile
        var tmp_dir_buf: [fs.max_path_bytes]u8 = undefined;
        const tmp_dir = try std.fmt.bufPrint(&tmp_dir_buf, "/tmp/silo-build-{d}", .{std.time.timestamp()});

        // Create temp dir
        try fs.makeDirAbsolute(tmp_dir);
        defer fs.deleteTreeAbsolute(tmp_dir) catch {};

        // Write Dockerfile
        const dockerfile_path = try std.fmt.allocPrint(allocator, "{s}/Dockerfile", .{tmp_dir});
        defer allocator.free(dockerfile_path);

        const file = try fs.createFileAbsolute(dockerfile_path, .{});
        defer file.close();
        try file.writeAll(dockerfile);

        // Build args
        var argv = std.ArrayList([]const u8).init(allocator);
        defer {
            for (argv.items) |item| {
                // Only free items we allocated
                if (mem.startsWith(u8, item, "--build-arg=")) {
                    allocator.free(item);
                }
            }
            argv.deinit();
        }

        try argv.append("docker");
        try argv.append("build");
        try argv.append("-f");
        try argv.append(dockerfile_path);
        try argv.append("--target");
        try argv.append(target);
        try argv.append("-t");
        try argv.append(target);

        var it = build_args.iterator();
        while (it.next()) |entry| {
            const arg = try std.fmt.allocPrint(allocator, "--build-arg={s}={s}", .{ entry.key_ptr.*, entry.value_ptr.* });
            try argv.append(arg);
        }

        try argv.append(tmp_dir);

        // Run docker build
        var child = std.process.Child.init(argv.items, allocator);
        child.stdout_behavior = .Pipe;
        child.stderr_behavior = .Pipe;

        try child.spawn();

        // Read and discard output (could parse for progress)
        const stdout_reader = child.stdout.?.reader();
        while (true) {
            _ = stdout_reader.readByte() catch break;
        }

        const result = try child.wait();
        if (result.Exited != 0) {
            cli.logError(stderr, "Docker build failed with exit code {d}", .{result.Exited});
            return error.DockerBuildFailed;
        }
    }

    /// Run Docker container
    pub fn run(allocator: Allocator, opts: RunOptions) !void {
        var argv = std.ArrayList([]const u8).init(allocator);
        defer {
            // Free only the strings we allocated
            for (argv.items, 0..) |item, i| {
                if (i > 0) { // Skip "docker"
                    if (mem.startsWith(u8, item, "-v") or
                        mem.startsWith(u8, item, "-e") or
                        mem.startsWith(u8, item, "-w") or
                        mem.startsWith(u8, item, "--name") or
                        mem.startsWith(u8, item, "--security-opt"))
                    {
                        allocator.free(item);
                    }
                }
            }
            argv.deinit();
        }

        try argv.append("docker");
        try argv.append("run");
        try argv.append("-it");
        try argv.append("--rm");

        // Name
        const name_arg = try std.fmt.allocPrint(allocator, "--name={s}", .{opts.name});
        try argv.append(name_arg);

        // Working directory
        const workdir_arg = try std.fmt.allocPrint(allocator, "-w={s}", .{opts.workdir});
        try argv.append(workdir_arg);

        // Security options
        for (opts.security_options) |sec_opt| {
            const sec_arg = try std.fmt.allocPrint(allocator, "--security-opt={s}", .{sec_opt});
            try argv.append(sec_arg);
        }
        try argv.append("--cap-drop=ALL");

        // Read-only mounts
        for (opts.mounts_ro) |mount_path| {
            // Check if path exists
            fs.accessAbsolute(mount_path, .{}) catch continue;
            const mount_arg = try std.fmt.allocPrint(allocator, "-v={s}:{s}:ro", .{ mount_path, mount_path });
            try argv.append(mount_arg);
        }

        // Read-write mounts
        for (opts.mounts_rw) |mount_path| {
            // Check if path exists
            fs.accessAbsolute(mount_path, .{}) catch continue;
            const mount_arg = try std.fmt.allocPrint(allocator, "-v={s}:{s}", .{ mount_path, mount_path });
            try argv.append(mount_arg);
        }

        // Environment variables
        for (opts.env) |env_var| {
            const env_arg = try std.fmt.allocPrint(allocator, "-e={s}", .{env_var});
            try argv.append(env_arg);
        }

        // Image
        try argv.append(opts.image);

        // Build command with prehooks
        if (opts.prehooks.len > 0 or opts.command.len > 0) {
            try argv.append("/bin/bash");
            try argv.append("-c");

            var script = std.ArrayList(u8).init(allocator);
            defer script.deinit();

            // Add prehooks
            for (opts.prehooks) |hook| {
                try script.appendSlice(hook);
                try script.appendSlice(" && ");
            }

            // Add main command
            try script.appendSlice("exec ");
            for (opts.command, 0..) |cmd_part, i| {
                if (i > 0) try script.append(' ');
                // Shell quote the command parts
                try script.append('\'');
                for (cmd_part) |c| {
                    if (c == '\'') {
                        try script.appendSlice("'\"'\"'");
                    } else {
                        try script.append(c);
                    }
                }
                try script.append('\'');
            }

            // Add args
            for (opts.args) |arg| {
                try script.append(' ');
                try script.append('\'');
                for (arg) |c| {
                    if (c == '\'') {
                        try script.appendSlice("'\"'\"'");
                    } else {
                        try script.append(c);
                    }
                }
                try script.append('\'');
            }

            const script_str = try script.toOwnedSlice();
            try argv.append(script_str);
        } else if (opts.args.len > 0) {
            for (opts.args) |arg| {
                try argv.append(arg);
            }
        }

        // Execute docker run (replace current process for proper TTY handling)
        const argv_z = try allocator.allocSentinel(?[*:0]const u8, argv.items.len, null);
        defer allocator.free(argv_z);

        for (argv.items, 0..) |arg, i| {
            argv_z[i] = try allocator.dupeZ(u8, arg);
        }

        const err = std.posix.execvpeZ(argv_z[0].?, argv_z, std.c.environ);
        return err;
    }

    pub const RunOptions = struct {
        image: []const u8,
        name: []const u8,
        workdir: []const u8,
        mounts_ro: []const []const u8,
        mounts_rw: []const []const u8,
        env: []const []const u8,
        command: []const []const u8,
        args: []const []const u8,
        prehooks: []const []const u8,
        security_options: []const []const u8,
    };

    /// Get git worktree roots
    pub fn getGitWorktreeRoots(allocator: Allocator, dir: []const u8) ![][]const u8 {
        var roots = std.ArrayList([]const u8).init(allocator);
        var seen = std.StringHashMap(void).init(allocator);
        defer seen.deinit();

        // Check current dir and immediate subdirs
        var dirs = std.ArrayList([]const u8).init(allocator);
        defer dirs.deinit();

        try dirs.append(dir);

        // Add subdirectories
        if (fs.openDirAbsolute(dir, .{ .iterate = true })) |d| {
            var dir_handle = d;
            defer dir_handle.close();

            var it = dir_handle.iterate();
            while (try it.next()) |entry| {
                if (entry.kind == .directory) {
                    const subdir = try std.fmt.allocPrint(allocator, "{s}/{s}", .{ dir, entry.name });
                    try dirs.append(subdir);
                }
            }
        } else |_| {}

        for (dirs.items) |d| {
            // Check if it's a git worktree
            const git_check_result = std.process.Child.run(.{
                .allocator = allocator,
                .argv = &.{ "git", "-C", d, "rev-parse", "--is-inside-work-tree" },
            }) catch continue;
            defer allocator.free(git_check_result.stdout);
            defer allocator.free(git_check_result.stderr);

            if (git_check_result.term.Exited != 0) continue;

            // Get git dir
            const git_dir_result = std.process.Child.run(.{
                .allocator = allocator,
                .argv = &.{ "git", "-C", d, "rev-parse", "--git-dir" },
            }) catch continue;
            defer allocator.free(git_dir_result.stdout);
            defer allocator.free(git_dir_result.stderr);

            const git_dir_raw = mem.trimRight(u8, git_dir_result.stdout, "\n\r ");
            const git_dir = if (!fs.path.isAbsolute(git_dir_raw))
                try std.fmt.allocPrint(allocator, "{s}/{s}", .{ d, git_dir_raw })
            else
                try allocator.dupe(u8, git_dir_raw);
            defer allocator.free(git_dir);

            // Get git common dir
            const git_common_result = std.process.Child.run(.{
                .allocator = allocator,
                .argv = &.{ "git", "-C", d, "rev-parse", "--git-common-dir" },
            }) catch continue;
            defer allocator.free(git_common_result.stdout);
            defer allocator.free(git_common_result.stderr);

            const git_common_raw = mem.trimRight(u8, git_common_result.stdout, "\n\r ");
            const git_common_dir = if (!fs.path.isAbsolute(git_common_raw))
                try std.fmt.allocPrint(allocator, "{s}/{s}", .{ d, git_common_raw })
            else
                try allocator.dupe(u8, git_common_raw);
            defer allocator.free(git_common_dir);

            // If git-dir != git-common-dir, it's a worktree
            if (!mem.eql(u8, git_dir, git_common_dir)) {
                const common_root = fs.path.dirname(git_common_dir) orelse continue;
                if (!seen.contains(common_root)) {
                    try seen.put(try allocator.dupe(u8, common_root), {});
                    try roots.append(try allocator.dupe(u8, common_root));
                }
            }
        }

        return try roots.toOwnedSlice();
    }

    pub const GitIdentity = struct {
        name: ?[]const u8 = null,
        email: ?[]const u8 = null,
    };

    /// Get git identity
    pub fn getGitIdentity(allocator: Allocator) !GitIdentity {
        var name: ?[]const u8 = null;
        var email: ?[]const u8 = null;

        // Get name
        const name_result = std.process.Child.run(.{
            .allocator = allocator,
            .argv = &.{ "git", "config", "--global", "user.name" },
        }) catch return .{ .name = null, .email = null };

        if (name_result.term.Exited == 0 and name_result.stdout.len > 0) {
            name = try allocator.dupe(u8, mem.trimRight(u8, name_result.stdout, "\n\r "));
        }
        allocator.free(name_result.stdout);
        allocator.free(name_result.stderr);

        // Get email
        const email_result = std.process.Child.run(.{
            .allocator = allocator,
            .argv = &.{ "git", "config", "--global", "user.email" },
        }) catch return .{ .name = name, .email = null };

        if (email_result.term.Exited == 0 and email_result.stdout.len > 0) {
            email = try allocator.dupe(u8, mem.trimRight(u8, email_result.stdout, "\n\r "));
        }
        allocator.free(email_result.stdout);
        allocator.free(email_result.stderr);

        return .{ .name = name, .email = email };
    }
};

// ============================================
// Tool Definitions
// ============================================

pub const tools = struct {
    pub const available = [_][]const u8{ "opencode", "claude", "copilot" };

    pub fn description(tool: []const u8) []const u8 {
        if (mem.eql(u8, tool, "opencode")) return "OpenCode - AI coding assistant";
        if (mem.eql(u8, tool, "claude")) return "Claude Code - Anthropic's CLI for Claude";
        if (mem.eql(u8, tool, "copilot")) return "GitHub Copilot CLI";
        return "Unknown tool";
    }

    pub fn isValid(tool: []const u8) bool {
        for (available) |t| {
            if (mem.eql(u8, tool, t)) return true;
        }
        return false;
    }

    pub fn getCommand(tool: []const u8, home: []const u8, allocator: Allocator) ![]const []const u8 {
        var cmd = std.ArrayList([]const u8).init(allocator);

        if (mem.eql(u8, tool, "claude")) {
            try cmd.append("claude");
            const mcp_config = try std.fmt.allocPrint(allocator, "--mcp-config={s}/.claude/mcp.json", .{home});
            try cmd.append(mcp_config);
            try cmd.append("--dangerously-skip-permissions");
        } else if (mem.eql(u8, tool, "opencode")) {
            try cmd.append("opencode");
        } else if (mem.eql(u8, tool, "copilot")) {
            try cmd.append("copilot");
            try cmd.append("--allow-all");
        }

        return try cmd.toOwnedSlice();
    }
};

// ============================================
// Sample Config
// ============================================

const sample_config =
    \\{
    \\  // Read-only directories or files to mount into the container
    \\  "mounts_ro": [],
    \\  // Read-write directories or files to mount into the container
    \\  "mounts_rw": [],
    \\  // Environment variables: names without '=' pass through from host,
    \\  // names with '=' set explicitly (e.g., "FOO=bar")
    \\  "env": [],
    \\  // Shell commands to run inside the container before the tool
    \\  "prehooks": [],
    \\  // Tool-specific configuration (merged with global config above)
    \\  // Example: "tools": { "claude": { "env": ["CLAUDE_SPECIFIC_VAR"] } }
    \\  "tools": {}
    \\}
;

// ============================================
// Main Entry Point
// ============================================

pub fn main() !u8 {
    var gpa = std.heap.GeneralPurposeAllocator(.{}){};
    defer _ = gpa.deinit();
    const allocator = gpa.allocator();

    const stderr = std.io.getStdErr().writer();
    const stdout = std.io.getStdOut().writer();

    // Parse command line arguments
    const args = try std.process.argsAlloc(allocator);
    defer std.process.argsFree(allocator, args);

    if (args.len < 2) {
        // Interactive mode - show tool selection
        return runInteractive(allocator, stderr);
    }

    const cmd = args[1];

    // Handle version
    if (mem.eql(u8, cmd, "--version") or mem.eql(u8, cmd, "-v")) {
        try stdout.print("silo version {s}\n", .{version});
        return 0;
    }

    // Handle help
    if (mem.eql(u8, cmd, "--help") or mem.eql(u8, cmd, "-h") or mem.eql(u8, cmd, "help")) {
        try printHelp(stdout);
        return 0;
    }

    // Handle config commands
    if (mem.eql(u8, cmd, "config")) {
        if (args.len < 3) {
            cli.logError(stderr, "config requires a subcommand: show, paths, edit, init, default", .{});
            return 1;
        }
        const subcmd = args[2];

        if (mem.eql(u8, subcmd, "show")) {
            return runConfigShow(allocator, stdout);
        } else if (mem.eql(u8, subcmd, "paths")) {
            return runConfigPaths(allocator, stdout);
        } else if (mem.eql(u8, subcmd, "default")) {
            return runConfigDefault(stdout);
        } else if (mem.eql(u8, subcmd, "init")) {
            const global_flag = blk: {
                for (args[3..]) |arg| {
                    if (mem.eql(u8, arg, "--global") or mem.eql(u8, arg, "-g")) break :blk true;
                }
                break :blk false;
            };
            const local_flag = blk: {
                for (args[3..]) |arg| {
                    if (mem.eql(u8, arg, "--local") or mem.eql(u8, arg, "-l")) break :blk true;
                }
                break :blk false;
            };
            return runConfigInit(allocator, stderr, global_flag, local_flag);
        } else if (mem.eql(u8, subcmd, "edit")) {
            return runConfigEdit(allocator, stderr);
        } else {
            cli.logError(stderr, "unknown config subcommand: {s}", .{subcmd});
            return 1;
        }
    }

    // Handle tool command
    if (tools.isValid(cmd)) {
        // Collect tool args (everything after --)
        var tool_args = std.ArrayList([]const u8).init(allocator);
        defer tool_args.deinit();

        var found_separator = false;
        for (args[2..]) |arg| {
            if (mem.eql(u8, arg, "--")) {
                found_separator = true;
                continue;
            }
            if (found_separator) {
                try tool_args.append(arg);
            }
        }

        return runTool(allocator, cmd, tool_args.items, stderr);
    }

    cli.logError(stderr, "unknown command: {s}", .{cmd});
    try printHelp(stderr);
    return 1;
}

fn printHelp(writer: anytype) !void {
    const help =
        \\
        \\  ███████╗██╗██╗      ██████╗
        \\  ██╔════╝██║██║     ██╔═══██╗
        \\  ███████╗██║██║     ██║   ██║
        \\  ╚════██║██║██║     ██║   ██║
        \\  ███████║██║███████╗╚██████╔╝
        \\  ╚══════╝╚═╝╚══════╝ ╚═════╝
        \\
        \\Run AI coding assistants (Claude Code, OpenCode, Copilot) in isolated
        \\Docker containers with proper security sandboxing.
        \\
        \\Usage: silo [command] [options]
        \\
        \\Commands:
        \\  claude              Run Claude Code
        \\  opencode            Run OpenCode
        \\  copilot             Run GitHub Copilot CLI
        \\  config show         Show merged configuration
        \\  config paths        Show config file paths
        \\  config edit         Edit a config file
        \\  config init         Create a sample config file
        \\  config default      Show default configuration
        \\
        \\Options:
        \\  -h, --help          Show this help
        \\  -v, --version       Show version
        \\
        \\Examples:
        \\  silo                Interactive tool selection
        \\  silo claude         Run Claude Code
        \\  silo claude -- --help  Pass arguments to Claude
        \\
    ;
    try writer.writeAll(help);
}

fn runInteractive(allocator: Allocator, stderr: anytype) !u8 {
    // Simple interactive selection using terminal
    const stdin = std.io.getStdIn();
    const stdout = std.io.getStdOut().writer();

    try stdout.writeAll("\nSelect AI Tool:\n\n");
    for (tools.available, 0..) |tool, i| {
        try stdout.print("  {d}) {s}\n", .{ i + 1, tools.description(tool) });
    }
    try stdout.writeAll("\nEnter choice (1-3): ");

    var buf: [10]u8 = undefined;
    const line = stdin.reader().readUntilDelimiter(&buf, '\n') catch {
        cli.logError(stderr, "selection cancelled", .{});
        return 1;
    };

    const choice = std.fmt.parseInt(usize, mem.trim(u8, line, " \t\r\n"), 10) catch {
        cli.logError(stderr, "invalid choice", .{});
        return 1;
    };

    if (choice < 1 or choice > tools.available.len) {
        cli.logError(stderr, "invalid choice", .{});
        return 1;
    }

    const selected_tool = tools.available[choice - 1];
    return runTool(allocator, selected_tool, &.{}, stderr);
}

fn runTool(allocator: Allocator, tool: []const u8, tool_args: []const []const u8, stderr: anytype) !u8 {
    // Load configuration
    var cfg = config.loadAll(allocator) catch |err| blk: {
        cli.logWarning(stderr, "Failed to load config: {}", .{err});
        break :blk config.defaultConfig(allocator) catch return 1;
    };
    defer cfg.deinit();

    // Get user info
    const home = std.posix.getenv("HOME") orelse "/tmp";
    const user = std.posix.getenv("USER") orelse "user";
    const uid = std.os.linux.getuid();

    // Build the image
    cli.log(stderr, "Connecting to Docker...", .{});
    cli.log(stderr, "Preparing image for {s}...", .{tool});

    var build_args = std.StringHashMap([]const u8).init(allocator);
    defer build_args.deinit();

    try build_args.put("HOME", home);
    try build_args.put("USER", user);
    const uid_str = try std.fmt.allocPrint(allocator, "{d}", .{uid});
    defer allocator.free(uid_str);
    try build_args.put("UID", uid_str);

    docker.build(allocator, dockerfile_content, tool, build_args, stderr) catch |err| {
        cli.logError(stderr, "Failed to build image: {}", .{err});
        return 1;
    };

    cli.logSuccess(stderr, "Image ready", .{});

    // Collect mounts
    var cwd_buf: [fs.max_path_bytes]u8 = undefined;
    const cwd = try std.posix.getcwd(&cwd_buf);

    var mounts_rw = std.ArrayList([]const u8).init(allocator);
    defer {
        for (mounts_rw.items) |m| allocator.free(m);
        mounts_rw.deinit();
    }
    var mounts_ro = std.ArrayList([]const u8).init(allocator);
    defer {
        for (mounts_ro.items) |m| allocator.free(m);
        mounts_ro.deinit();
    }

    // Add cwd as read-write mount
    try mounts_rw.append(try allocator.dupe(u8, cwd));

    // Add tool-specific mounts
    if (cfg.getToolConfig(tool)) |tool_cfg| {
        for (tool_cfg.mounts_ro) |m| {
            const expanded = try config.expandPath(allocator, m);
            try mounts_ro.append(expanded);
        }
        for (tool_cfg.mounts_rw) |m| {
            const expanded = try config.expandPath(allocator, m);
            try mounts_rw.append(expanded);
        }
    }

    // Add global mounts
    for (cfg.mounts_ro.items) |m| {
        const expanded = try config.expandPath(allocator, m);
        try mounts_ro.append(expanded);
    }
    for (cfg.mounts_rw.items) |m| {
        const expanded = try config.expandPath(allocator, m);
        try mounts_rw.append(expanded);
    }

    // Add git worktree roots
    const worktree_roots = docker.getGitWorktreeRoots(allocator, cwd) catch &.{};
    defer {
        for (worktree_roots) |r| allocator.free(r);
        allocator.free(worktree_roots);
    }
    for (worktree_roots) |root| {
        try mounts_rw.append(try allocator.dupe(u8, root));
    }

    // Collect environment variables
    var env_vars = std.ArrayList([]const u8).init(allocator);
    defer {
        for (env_vars.items) |e| allocator.free(e);
        env_vars.deinit();
    }

    // Get git identity
    const git_identity = docker.getGitIdentity(allocator) catch docker.GitIdentity{};
    defer {
        if (git_identity.name) |n| allocator.free(n);
        if (git_identity.email) |e| allocator.free(e);
    }

    if (git_identity.name) |name| {
        try env_vars.append(try std.fmt.allocPrint(allocator, "GIT_AUTHOR_NAME={s}", .{name}));
        try env_vars.append(try std.fmt.allocPrint(allocator, "GIT_COMMITTER_NAME={s}", .{name}));
        if (git_identity.email) |email| {
            cli.log(stderr, "Git identity: {s} <{s}>", .{ name, email });
        }
    }
    if (git_identity.email) |email| {
        try env_vars.append(try std.fmt.allocPrint(allocator, "GIT_AUTHOR_EMAIL={s}", .{email}));
        try env_vars.append(try std.fmt.allocPrint(allocator, "GIT_COMMITTER_EMAIL={s}", .{email}));
    }

    // Process global env vars
    for (cfg.env.items) |e| {
        if (mem.indexOf(u8, e, "=")) |_| {
            try env_vars.append(try allocator.dupe(u8, e));
        } else {
            if (std.posix.getenv(e)) |val| {
                try env_vars.append(try std.fmt.allocPrint(allocator, "{s}={s}", .{ e, val }));
            }
        }
    }

    // Tool-specific env vars
    if (cfg.getToolConfig(tool)) |tool_cfg| {
        for (tool_cfg.env) |e| {
            if (mem.indexOf(u8, e, "=")) |_| {
                try env_vars.append(try allocator.dupe(u8, e));
            } else {
                if (std.posix.getenv(e)) |val| {
                    try env_vars.append(try std.fmt.allocPrint(allocator, "{s}={s}", .{ e, val }));
                }
            }
        }
    }

    // Generate container name
    const base_name = fs.path.basename(cwd);
    var clean_name = std.ArrayList(u8).init(allocator);
    defer clean_name.deinit();
    for (base_name) |c| {
        if (c != '.') try clean_name.append(c);
    }

    var prng = std.Random.DefaultPrng.init(@intCast(std.time.timestamp()));
    const random_suffix = prng.random().intRangeAtMost(u8, 0, 99);
    const container_name = try std.fmt.allocPrint(allocator, "{s}-{d:0>2}", .{ clean_name.items, random_suffix });
    defer allocator.free(container_name);

    // Log mounts
    var seen = std.StringHashMap(void).init(allocator);
    defer seen.deinit();

    if (mounts_ro.items.len > 0) {
        cli.log(stderr, "Mounts (read-only):", .{});
        for (mounts_ro.items) |m| {
            fs.accessAbsolute(m, .{}) catch continue;
            if (seen.contains(m)) continue;
            try seen.put(m, {});
            cli.logBullet(stderr, "{s}", .{m});
        }
    }

    cli.log(stderr, "Mounts (read-write):", .{});
    for (mounts_rw.items) |m| {
        fs.accessAbsolute(m, .{}) catch continue;
        if (seen.contains(m)) continue;
        try seen.put(m, {});
        cli.logBullet(stderr, "{s}", .{m});
    }

    cli.log(stderr, "Container name: {s}", .{container_name});
    cli.log(stderr, "Running {s}...", .{tool});

    // Get tool command
    const tool_command = try tools.getCommand(tool, home, allocator);
    defer {
        for (tool_command) |c| allocator.free(c);
        allocator.free(tool_command);
    }

    // Run container (this replaces the current process)
    docker.run(allocator, .{
        .image = tool,
        .name = container_name,
        .workdir = cwd,
        .mounts_ro = mounts_ro.items,
        .mounts_rw = mounts_rw.items,
        .env = env_vars.items,
        .command = tool_command,
        .args = tool_args,
        .prehooks = cfg.prehooks.items,
        .security_options = &.{"no-new-privileges:true"},
    }) catch |err| {
        cli.logError(stderr, "Container error: {}", .{err});
        return 1;
    };

    return 0;
}

fn runConfigShow(allocator: Allocator, stdout: anytype) !u8 {
    var cfg = config.loadAll(allocator) catch |err| {
        cli.logError(std.io.getStdErr().writer(), "Failed to load config: {}", .{err});
        return 1;
    };
    defer cfg.deinit();

    // Output as JSON (simplified version without source tracking)
    try stdout.writeAll("{\n");

    // MountsRO
    try stdout.writeAll("  \"mounts_ro\": [\n");
    for (cfg.mounts_ro.items, 0..) |v, i| {
        const comma: []const u8 = if (i < cfg.mounts_ro.items.len - 1) "," else "";
        try stdout.print("    \"{s}\"{s}\n", .{ v, comma });
    }
    try stdout.writeAll("  ],\n");

    // MountsRW
    try stdout.writeAll("  \"mounts_rw\": [\n");
    for (cfg.mounts_rw.items, 0..) |v, i| {
        const comma: []const u8 = if (i < cfg.mounts_rw.items.len - 1) "," else "";
        try stdout.print("    \"{s}\"{s}\n", .{ v, comma });
    }
    try stdout.writeAll("  ],\n");

    // Env
    try stdout.writeAll("  \"env\": [\n");
    for (cfg.env.items, 0..) |v, i| {
        const comma: []const u8 = if (i < cfg.env.items.len - 1) "," else "";
        try stdout.print("    \"{s}\"{s}\n", .{ v, comma });
    }
    try stdout.writeAll("  ],\n");

    // Prehooks
    try stdout.writeAll("  \"prehooks\": [\n");
    for (cfg.prehooks.items, 0..) |v, i| {
        const comma: []const u8 = if (i < cfg.prehooks.items.len - 1) "," else "";
        try stdout.print("    \"{s}\"{s}\n", .{ v, comma });
    }
    try stdout.writeAll("  ],\n");

    // Tools
    try stdout.writeAll("  \"tools\": {\n");

    var tool_names = std.ArrayList([]const u8).init(allocator);
    defer tool_names.deinit();

    var it = cfg.tools.iterator();
    while (it.next()) |entry| {
        try tool_names.append(entry.key_ptr.*);
    }

    mem.sort([]const u8, tool_names.items, {}, struct {
        fn lessThan(_: void, a: []const u8, b: []const u8) bool {
            return mem.lessThan(u8, a, b);
        }
    }.lessThan);

    for (tool_names.items, 0..) |tool_name, ti| {
        const tool_cfg = cfg.tools.get(tool_name).?;

        try stdout.print("    \"{s}\": {{\n", .{tool_name});

        // Tool mounts_ro
        try stdout.writeAll("      \"mounts_ro\": [\n");
        for (tool_cfg.mounts_ro, 0..) |v, i| {
            const comma: []const u8 = if (i < tool_cfg.mounts_ro.len - 1) "," else "";
            try stdout.print("        \"{s}\"{s}\n", .{ v, comma });
        }
        try stdout.writeAll("      ],\n");

        // Tool mounts_rw
        try stdout.writeAll("      \"mounts_rw\": [\n");
        for (tool_cfg.mounts_rw, 0..) |v, i| {
            const comma: []const u8 = if (i < tool_cfg.mounts_rw.len - 1) "," else "";
            try stdout.print("        \"{s}\"{s}\n", .{ v, comma });
        }
        try stdout.writeAll("      ],\n");

        // Tool env
        try stdout.writeAll("      \"env\": [\n");
        for (tool_cfg.env, 0..) |v, i| {
            const comma: []const u8 = if (i < tool_cfg.env.len - 1) "," else "";
            try stdout.print("        \"{s}\"{s}\n", .{ v, comma });
        }
        try stdout.writeAll("      ]\n");

        const tool_comma: []const u8 = if (ti < tool_names.items.len - 1) "," else "";
        try stdout.print("    }}{s}\n", .{tool_comma});
    }

    try stdout.writeAll("  }\n");
    try stdout.writeAll("}\n");

    return 0;
}

fn runConfigPaths(allocator: Allocator, stdout: anytype) !u8 {
    const paths = try config.getConfigPaths(allocator);
    defer {
        for (paths) |p| allocator.free(p.path);
        allocator.free(paths);
    }

    for (paths) |p| {
        if (p.exists) {
            try stdout.print("{s}\n", .{p.path});
        }
    }

    return 0;
}

fn runConfigDefault(stdout: anytype) !u8 {
    // Just print the sample config as default
    try stdout.writeAll(sample_config);
    try stdout.writeAll("\n");
    return 0;
}

fn runConfigInit(allocator: Allocator, stderr: anytype, global_flag: bool, local_flag: bool) !u8 {
    var config_path: []const u8 = undefined;
    var should_free = false;

    if (global_flag) {
        const xdg_config = try config.getXDGConfigHome(allocator);
        defer allocator.free(xdg_config);

        const config_dir = try std.fmt.allocPrint(allocator, "{s}/silo", .{xdg_config});
        defer allocator.free(config_dir);

        fs.makeDirAbsolute(config_dir) catch |err| {
            if (err != error.PathAlreadyExists) {
                cli.logError(stderr, "Failed to create config directory: {}", .{err});
                return 1;
            }
        };

        config_path = try std.fmt.allocPrint(allocator, "{s}/silo.jsonc", .{config_dir});
        should_free = true;
    } else if (local_flag) {
        config_path = "silo.jsonc";
    } else {
        // Interactive selection
        const stdin = std.io.getStdIn();
        const stdout = std.io.getStdOut().writer();

        try stdout.writeAll("\nCreate Configuration:\n\n");
        try stdout.writeAll("  1) Local (silo.jsonc in current directory)\n");
        try stdout.writeAll("  2) Global (~/.config/silo/silo.jsonc)\n");
        try stdout.writeAll("\nEnter choice (1-2): ");

        var buf: [10]u8 = undefined;
        const line = stdin.reader().readUntilDelimiter(&buf, '\n') catch {
            cli.logError(stderr, "selection cancelled", .{});
            return 1;
        };

        const choice = std.fmt.parseInt(usize, mem.trim(u8, line, " \t\r\n"), 10) catch {
            cli.logError(stderr, "invalid choice", .{});
            return 1;
        };

        if (choice == 1) {
            config_path = "silo.jsonc";
        } else if (choice == 2) {
            const xdg_config = try config.getXDGConfigHome(allocator);
            defer allocator.free(xdg_config);

            const config_dir = try std.fmt.allocPrint(allocator, "{s}/silo", .{xdg_config});
            defer allocator.free(config_dir);

            fs.makeDirAbsolute(config_dir) catch |err| {
                if (err != error.PathAlreadyExists) {
                    cli.logError(stderr, "Failed to create config directory: {}", .{err});
                    return 1;
                }
            };

            config_path = try std.fmt.allocPrint(allocator, "{s}/silo.jsonc", .{config_dir});
            should_free = true;
        } else {
            cli.logError(stderr, "invalid choice", .{});
            return 1;
        }
    }

    defer if (should_free) allocator.free(config_path);

    // Check if file already exists
    if (fs.path.isAbsolute(config_path)) {
        fs.accessAbsolute(config_path, .{}) catch |err| {
            if (err != error.FileNotFound) {
                cli.logError(stderr, "config file already exists: {s}", .{config_path});
                return 1;
            }
        };
    } else {
        var cwd_buf: [fs.max_path_bytes]u8 = undefined;
        const cwd = try std.posix.getcwd(&cwd_buf);
        const full_path = try std.fmt.allocPrint(allocator, "{s}/{s}", .{ cwd, config_path });
        defer allocator.free(full_path);

        fs.accessAbsolute(full_path, .{}) catch |err| {
            if (err == error.FileNotFound) {
                // File doesn't exist, we can create it
                const file = fs.createFileAbsolute(full_path, .{}) catch |e| {
                    cli.logError(stderr, "Failed to create config: {}", .{e});
                    return 1;
                };
                defer file.close();
                file.writeAll(sample_config) catch |e| {
                    cli.logError(stderr, "Failed to write config: {}", .{e});
                    return 1;
                };
                cli.logSuccess(stderr, "Created {s}", .{config_path});
                return 0;
            }
        };

        cli.logError(stderr, "config file already exists: {s}", .{config_path});
        return 1;
    }

    // Create file for absolute path
    const file = fs.createFileAbsolute(config_path, .{}) catch |err| {
        cli.logError(stderr, "Failed to create config: {}", .{err});
        return 1;
    };
    defer file.close();

    file.writeAll(sample_config) catch |err| {
        cli.logError(stderr, "Failed to write config: {}", .{err});
        return 1;
    };

    cli.logSuccess(stderr, "Created {s}", .{config_path});
    return 0;
}

fn runConfigEdit(allocator: Allocator, stderr: anytype) !u8 {
    const paths = try config.getConfigPaths(allocator);
    defer {
        for (paths) |p| allocator.free(p.path);
        allocator.free(paths);
    }

    // Build options for selection
    var options = std.ArrayList(struct { path: []const u8, is_new: bool }).init(allocator);
    defer options.deinit();

    for (paths, 0..) |p, i| {
        const is_global = (i == 0);
        if (!is_global and !p.exists) continue;
        try options.append(.{ .path = p.path, .is_new = !p.exists });
    }

    if (options.items.len == 0) {
        cli.logError(stderr, "No config files available", .{});
        return 1;
    }

    // Interactive selection
    const stdin = std.io.getStdIn();
    const stdout = std.io.getStdOut().writer();

    try stdout.writeAll("\nSelect Config to Edit:\n\n");
    for (options.items, 0..) |opt, i| {
        const label: []const u8 = if (opt.is_new) " (new)" else "";
        try stdout.print("  {d}) {s}{s}\n", .{ i + 1, opt.path, label });
    }
    try stdout.writeAll("\nEnter choice: ");

    var buf: [10]u8 = undefined;
    const line = stdin.reader().readUntilDelimiter(&buf, '\n') catch {
        cli.logError(stderr, "selection cancelled", .{});
        return 1;
    };

    const choice = std.fmt.parseInt(usize, mem.trim(u8, line, " \t\r\n"), 10) catch {
        cli.logError(stderr, "invalid choice", .{});
        return 1;
    };

    if (choice < 1 or choice > options.items.len) {
        cli.logError(stderr, "invalid choice", .{});
        return 1;
    }

    const selected = options.items[choice - 1];

    // Ensure parent directory exists
    if (fs.path.dirname(selected.path)) |dir| {
        fs.makeDirAbsolute(dir) catch |err| {
            if (err != error.PathAlreadyExists) {
                cli.logError(stderr, "Failed to create directory: {}", .{err});
                return 1;
            }
        };
    }

    // If file doesn't exist, pre-fill with template
    if (selected.is_new) {
        const file = fs.createFileAbsolute(selected.path, .{}) catch |err| {
            cli.logError(stderr, "Failed to create config: {}", .{err});
            return 1;
        };
        defer file.close();
        file.writeAll(sample_config) catch |err| {
            cli.logError(stderr, "Failed to write config: {}", .{err});
            return 1;
        };
    }

    // Get editor
    const editor = std.posix.getenv("EDITOR") orelse std.posix.getenv("VISUAL") orelse "vi";

    // Open editor
    var child = std.process.Child.init(&.{ editor, selected.path }, allocator);
    child.stdin_behavior = .Inherit;
    child.stdout_behavior = .Inherit;
    child.stderr_behavior = .Inherit;

    try child.spawn();
    const result = try child.wait();

    if (result.Exited != 0) {
        cli.logError(stderr, "editor failed with exit code {d}", .{result.Exited});
        return 1;
    }

    return 0;
}

test "stripJsonComments" {
    const allocator = std.testing.allocator;

    const input =
        \\{
        \\  // This is a comment
        \\  "key": "value", /* inline comment */
        \\  "arr": [1, 2, 3]
        \\}
    ;

    const result = try config.stripJsonComments(allocator, input);
    defer allocator.free(result);

    // The result should be valid JSON
    const parsed = try json.parseFromSlice(json.Value, allocator, result, .{});
    defer parsed.deinit();

    try std.testing.expect(parsed.value == .object);
}

test "expandPath" {
    const allocator = std.testing.allocator;

    // Test with tilde
    const home = std.posix.getenv("HOME") orelse "/tmp";
    const expanded = try config.expandPath(allocator, "~/test");
    defer allocator.free(expanded);

    const expected = try std.fmt.allocPrint(allocator, "{s}/test", .{home});
    defer allocator.free(expected);

    try std.testing.expectEqualStrings(expected, expanded);
}
