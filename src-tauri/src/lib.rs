use std::sync::Mutex;

use tauri::{Manager, RunEvent};
use tauri_plugin_shell::process::CommandChild;
use tauri_plugin_shell::ShellExt;

const SIDECAR_PORT: &str = "17389";

struct SidecarProcess(Mutex<Option<CommandChild>>);

fn spawn_sidecar(app: &tauri::AppHandle) -> Result<CommandChild, Box<dyn std::error::Error>> {
    let mut args: Vec<String> = vec!["--port".into(), SIDECAR_PORT.into()];

    // 开发时 cwd 是 src-tauri，仓库根是它的父目录
    if let Ok(cwd) = std::env::current_dir() {
        let root = if cwd.file_name().and_then(|n| n.to_str()) == Some("src-tauri") {
            cwd.parent().map(|p| p.to_path_buf()).unwrap_or(cwd)
        } else {
            cwd
        };
        if root.join("package.json").exists() {
            args.push("--workdir".into());
            args.push(root.to_string_lossy().to_string());
        }
    }

    let (mut rx, child) = app.shell().sidecar("sidecar")?.args(args).spawn()?;

    tauri::async_runtime::spawn(async move {
        use tauri_plugin_shell::process::CommandEvent;
        while let Some(event) = rx.recv().await {
            match event {
                CommandEvent::Stdout(line) => {
                    println!("[sidecar] {}", String::from_utf8_lossy(&line).trim_end());
                }
                CommandEvent::Stderr(line) => {
                    eprintln!("[sidecar] {}", String::from_utf8_lossy(&line).trim_end());
                }
                CommandEvent::Terminated(payload) => {
                    eprintln!("[sidecar] terminated: {:?}", payload.code);
                    break;
                }
                _ => {}
            }
        }
    });

    Ok(child)
}

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    tauri::Builder::default()
        .plugin(tauri_plugin_shell::init())
        .manage(SidecarProcess(Mutex::new(None)))
        .setup(|app| {
            if cfg!(debug_assertions) {
                app.handle().plugin(
                    tauri_plugin_log::Builder::default()
                        .level(log::LevelFilter::Info)
                        .build(),
                )?;
            }

            let handle = app.handle().clone();
            match spawn_sidecar(&handle) {
                Ok(child) => {
                    println!("[tauri] sidecar spawned, pid={}", child.pid());
                    app.state::<SidecarProcess>().0.lock().unwrap().replace(child);
                }
                Err(err) => {
                    eprintln!("[tauri] failed to spawn sidecar: {err}");
                }
            }

            Ok(())
        })
        .build(tauri::generate_context!())
        .expect("error while building tauri application")
        .run(|app, event| {
            if let RunEvent::Exit = event {
                if let Some(child) = app.state::<SidecarProcess>().0.lock().unwrap().take() {
                    let _ = child.kill();
                    println!("[tauri] sidecar killed on exit");
                }
            }
        });
}