import Foundation
#if canImport(Darwin)
import Darwin
#elseif canImport(Glibc)
import Glibc
#endif

/// Error types for Docker operations
public enum DockerError: Error, LocalizedError {
    case connectionFailed(String)
    case buildFailed(String)
    case runFailed(String)
    case containerError(Int32)

    public var errorDescription: String? {
        switch self {
        case .connectionFailed(let msg):
            return "Failed to connect to Docker: \(msg)"
        case .buildFailed(let msg):
            return "Failed to build image: \(msg)"
        case .runFailed(let msg):
            return "Container run failed: \(msg)"
        case .containerError(let code):
            return "Container exited with status \(code)"
        }
    }
}

/// Options for building a Docker image
public struct BuildOptions {
    public var dockerfile: String
    public var target: String
    public var buildArgs: [String: String]
    public var onProgress: ((String) -> Void)?

    public init(
        dockerfile: String,
        target: String,
        buildArgs: [String: String] = [:],
        onProgress: ((String) -> Void)? = nil
    ) {
        self.dockerfile = dockerfile
        self.target = target
        self.buildArgs = buildArgs
        self.onProgress = onProgress
    }
}

/// Options for running a Docker container
public struct RunOptions {
    public var image: String
    public var name: String
    public var workDir: String
    public var mountsRO: [String]
    public var mountsRW: [String]
    public var env: [String]
    public var command: [String]
    public var args: [String]
    public var prehooks: [String]
    public var tty: Bool
    public var removeOnExit: Bool
    public var securityOptions: [String]

    public init(
        image: String,
        name: String,
        workDir: String,
        mountsRO: [String] = [],
        mountsRW: [String] = [],
        env: [String] = [],
        command: [String] = [],
        args: [String] = [],
        prehooks: [String] = [],
        tty: Bool = true,
        removeOnExit: Bool = true,
        securityOptions: [String] = []
    ) {
        self.image = image
        self.name = name
        self.workDir = workDir
        self.mountsRO = mountsRO
        self.mountsRW = mountsRW
        self.env = env
        self.command = command
        self.args = args
        self.prehooks = prehooks
        self.tty = tty
        self.removeOnExit = removeOnExit
        self.securityOptions = securityOptions
    }
}

/// Docker client for building images and running containers
public class DockerClient {
    private var cancelled = false

    public init() throws {
        // Verify Docker is available
        let result = runCommand("docker", arguments: ["version", "--format", "{{.Server.Version}}"])
        guard result.exitCode == 0 else {
            throw DockerError.connectionFailed("Docker not available or not running")
        }
    }

    /// Build a Docker image
    public func build(_ options: BuildOptions) throws -> String {
        // Create a temporary directory for the build context
        let tempDir = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try FileManager.default.createDirectory(at: tempDir, withIntermediateDirectories: true)
        defer {
            try? FileManager.default.removeItem(at: tempDir)
        }

        // Write the Dockerfile
        let dockerfilePath = tempDir.appendingPathComponent("Dockerfile")
        try options.dockerfile.write(to: dockerfilePath, atomically: true, encoding: .utf8)

        // Build arguments
        var arguments = ["build"]
        arguments.append("-f")
        arguments.append(dockerfilePath.path)
        arguments.append("--target")
        arguments.append(options.target)
        arguments.append("-t")
        arguments.append(options.target)

        for (key, value) in options.buildArgs {
            arguments.append("--build-arg")
            arguments.append("\(key)=\(value)")
        }

        arguments.append(tempDir.path)

        // Run docker build
        let result = runCommand("docker", arguments: arguments)
        if let progress = options.onProgress {
            progress(result.output)
        }

        guard result.exitCode == 0 else {
            throw DockerError.buildFailed(result.error.isEmpty ? result.output : result.error)
        }

        return options.target
    }

    /// Run a Docker container
    public func run(_ options: RunOptions) throws {
        var arguments = ["run"]

        // Name
        arguments.append("--name")
        arguments.append(options.name)

        // TTY and interactive
        if options.tty {
            arguments.append("-it")
        }

        // Remove on exit
        if options.removeOnExit {
            arguments.append("--rm")
        }

        // Working directory
        arguments.append("-w")
        arguments.append(options.workDir)

        // Security options
        arguments.append("--privileged=false")
        arguments.append("--cap-drop=ALL")
        for opt in options.securityOptions {
            arguments.append("--security-opt")
            arguments.append(opt)
        }

        // Mounts - read only
        for mount in options.mountsRO {
            // Skip non-existent paths
            guard FileManager.default.fileExists(atPath: mount) else { continue }
            arguments.append("-v")
            arguments.append("\(mount):\(mount):ro")
        }

        // Mounts - read write
        for mount in options.mountsRW {
            // Skip non-existent paths
            guard FileManager.default.fileExists(atPath: mount) else { continue }
            arguments.append("-v")
            arguments.append("\(mount):\(mount)")
        }

        // Environment variables
        for envVar in options.env {
            arguments.append("-e")
            arguments.append(envVar)
        }

        // Image
        arguments.append(options.image)

        // Build command with prehooks
        if !options.command.isEmpty {
            let fullCmd = options.command + options.args

            if !options.prehooks.isEmpty {
                // Create a bash script that runs prehooks then execs the command
                var script = ""
                for hook in options.prehooks {
                    script += "\(hook) && "
                }
                script += "exec "
                script += shellQuote(fullCmd)

                arguments.append("/bin/bash")
                arguments.append("-c")
                arguments.append(script)
            } else {
                // No prehooks, just run the command directly
                arguments.append(contentsOf: fullCmd)
            }
        } else {
            // No command specified, just pass args
            arguments.append(contentsOf: options.args)
        }

        // Run docker interactively
        let exitCode = runInteractive("docker", arguments: arguments)

        if exitCode != 0 {
            throw DockerError.containerError(exitCode)
        }
    }

    /// Shell quote an array of strings
    private func shellQuote(_ args: [String]) -> String {
        return args.map { arg in
            // If the string contains special characters, quote it
            if arg.contains(where: { " \t\n\"'`$\\!*?[]{}()#&|;<>".contains($0) }) {
                let escaped = arg
                    .replacingOccurrences(of: "'", with: "'\"'\"'")
                return "'\(escaped)'"
            }
            return arg
        }.joined(separator: " ")
    }

    /// Run a command and capture output
    private func runCommand(_ command: String, arguments: [String]) -> (exitCode: Int32, output: String, error: String) {
        let process = Process()
        process.executableURL = URL(fileURLWithPath: "/usr/bin/env")
        process.arguments = [command] + arguments

        let outputPipe = Pipe()
        let errorPipe = Pipe()
        process.standardOutput = outputPipe
        process.standardError = errorPipe

        do {
            try process.run()
            process.waitUntilExit()
        } catch {
            return (-1, "", error.localizedDescription)
        }

        let outputData = outputPipe.fileHandleForReading.readDataToEndOfFile()
        let errorData = errorPipe.fileHandleForReading.readDataToEndOfFile()

        let output = String(data: outputData, encoding: .utf8) ?? ""
        let errorOutput = String(data: errorData, encoding: .utf8) ?? ""

        return (process.terminationStatus, output, errorOutput)
    }

    /// Run a command interactively (with TTY passthrough)
    private func runInteractive(_ command: String, arguments: [String]) -> Int32 {
        // Use execvp for full TTY passthrough
        let args = [command] + arguments
        let cArgs = args.map { strdup($0) } + [nil]

        // Fork the process
        let pid = fork()
        if pid == 0 {
            // Child process - exec docker
            execvp(command, cArgs)
            // If we get here, exec failed
            _exit(127)
        } else if pid > 0 {
            // Parent process - wait for child
            var status: Int32 = 0

            // Set up signal handlers to forward signals to child
            signal(SIGINT, SIG_IGN)
            signal(SIGTERM, SIG_IGN)

            // Handle SIGWINCH for terminal resize
            signal(SIGWINCH, SIG_IGN)

            waitpid(pid, &status, 0)

            // Restore signal handlers
            signal(SIGINT, SIG_DFL)
            signal(SIGTERM, SIG_DFL)

            if WIFEXITED(status) {
                return WEXITSTATUS(status)
            } else if WIFSIGNALED(status) {
                return Int32(128 + WTERMSIG(status))
            }
            return -1
        } else {
            // Fork failed
            return -1
        }
    }

    // C macros as Swift functions
    private func WIFEXITED(_ status: Int32) -> Bool {
        return (status & 0x7f) == 0
    }

    private func WEXITSTATUS(_ status: Int32) -> Int32 {
        return (status >> 8) & 0xff
    }

    private func WIFSIGNALED(_ status: Int32) -> Bool {
        return ((status & 0x7f) + 1) >> 1 > 0
    }

    private func WTERMSIG(_ status: Int32) -> Int32 {
        return status & 0x7f
    }
}

/// Get git worktree common directories for the given directory
public func getGitWorktreeRoots(dir: String) -> [String] {
    var roots: [String] = []
    var seen = Set<String>()

    // Check current dir and immediate subdirs
    var dirs = [dir]
    if let entries = try? FileManager.default.contentsOfDirectory(atPath: dir) {
        for entry in entries {
            let fullPath = (dir as NSString).appendingPathComponent(entry)
            var isDir: ObjCBool = false
            if FileManager.default.fileExists(atPath: fullPath, isDirectory: &isDir), isDir.boolValue {
                dirs.append(fullPath)
            }
        }
    }

    for d in dirs {
        // Check if it's a git worktree
        let isGitResult = runGitCommand(["-C", d, "rev-parse", "--is-inside-work-tree"])
        guard isGitResult.exitCode == 0 else { continue }

        // Get git dir
        let gitDirResult = runGitCommand(["-C", d, "rev-parse", "--git-dir"])
        guard gitDirResult.exitCode == 0 else { continue }
        var gitDir = gitDirResult.output.trimmingCharacters(in: .whitespacesAndNewlines)
        if !gitDir.hasPrefix("/") {
            gitDir = (d as NSString).appendingPathComponent(gitDir)
        }
        gitDir = (gitDir as NSString).standardizingPath

        // Get git common dir
        let gitCommonDirResult = runGitCommand(["-C", d, "rev-parse", "--git-common-dir"])
        guard gitCommonDirResult.exitCode == 0 else { continue }
        var gitCommonDir = gitCommonDirResult.output.trimmingCharacters(in: .whitespacesAndNewlines)
        if !gitCommonDir.hasPrefix("/") {
            gitCommonDir = (d as NSString).appendingPathComponent(gitCommonDir)
        }
        gitCommonDir = (gitCommonDir as NSString).standardizingPath

        // If git-dir != git-common-dir, it's a worktree
        if gitDir != gitCommonDir {
            let commonRoot = (gitCommonDir as NSString).deletingLastPathComponent
            if !seen.contains(commonRoot) {
                seen.insert(commonRoot)
                roots.append(commonRoot)
            }
        }
    }

    return roots
}

/// Get the git user.name and user.email from global config
public func getGitIdentity() -> (name: String, email: String) {
    let nameResult = runGitCommand(["config", "--global", "user.name"])
    let emailResult = runGitCommand(["config", "--global", "user.email"])

    let name = nameResult.exitCode == 0
        ? nameResult.output.trimmingCharacters(in: .whitespacesAndNewlines)
        : ""
    let email = emailResult.exitCode == 0
        ? emailResult.output.trimmingCharacters(in: .whitespacesAndNewlines)
        : ""

    return (name, email)
}

/// Run a git command and return the result
private func runGitCommand(_ arguments: [String]) -> (exitCode: Int32, output: String) {
    let process = Process()
    process.executableURL = URL(fileURLWithPath: "/usr/bin/git")
    process.arguments = arguments

    let pipe = Pipe()
    process.standardOutput = pipe
    process.standardError = FileHandle.nullDevice

    do {
        try process.run()
        process.waitUntilExit()
    } catch {
        return (-1, "")
    }

    let data = pipe.fileHandleForReading.readDataToEndOfFile()
    let output = String(data: data, encoding: .utf8) ?? ""

    return (process.terminationStatus, output)
}
