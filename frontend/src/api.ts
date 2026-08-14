import { Browser, Clipboard, Events } from '@wailsio/runtime'
import * as DesktopService from '../bindings/github.com/kangzyz/KSpeech/desktopservice'
import type { AppNotification, AppSnapshot, AssistantState, AudioChannelInput, LiveState } from './types'

export const api = {
  bootstrap: () => DesktopService.Bootstrap() as Promise<AppSnapshot>,
  start: () => DesktopService.Start() as Promise<void>,
  pause: () => DesktopService.Pause() as Promise<void>,
  stop: () => DesktopService.Stop() as Promise<void>,
  setLocked: (locked: boolean) => DesktopService.SetLocked(locked) as Promise<void>,
  setConfig: (key: string, value: unknown) => DesktopService.SetConfig(key, value) as Promise<void>,
  setPluginConfig: (key: string, values: Record<string, unknown>) =>
    DesktopService.SetPluginConfig(key, values) as Promise<void>,
  setAudioChannels: (channels: AudioChannelInput[]) =>
    DesktopService.SetAudioChannels(channels) as Promise<void>,
  openSettings: () => DesktopService.OpenSettings() as Promise<void>,
  askAssistant: (question: string) => DesktopService.AskAssistant(question) as Promise<void>,
  testAssistant: () => DesktopService.TestAssistant() as Promise<string>,
  clearAssistant: () => DesktopService.ClearAssistant() as Promise<void>,
  showConsole: () => DesktopService.ShowConsole() as Promise<void>,
  hideConsole: () => DesktopService.HideConsole() as Promise<void>,
  resetWindow: () => DesktopService.ResetConsoleWindow() as Promise<void>,
  copyHistory: () => DesktopService.CopyHistory() as Promise<void>,
  copyText: (text: string) => Clipboard.SetText(text),
  // 交给系统默认浏览器打开。调用方只应传 richText.ts 校验过的 http/https 地址：
  // 主窗口自己不能导航，否则整个应用界面就被网页顶掉了。
  openExternal: (url: string) => Browser.OpenURL(url),
  refreshResources: () => DesktopService.RefreshResources() as Promise<void>,
  installResource: (id: string) => DesktopService.InstallResource(id) as Promise<void>,
  removeResource: (id: string) => DesktopService.RemoveResource(id) as Promise<void>,
  quit: () => DesktopService.Quit() as Promise<void>,
  onState: (callback: (snapshot: AppSnapshot) => void) =>
    Events.On('kspeech:state', (event) => callback(event.data as AppSnapshot)),
  onLiveState: (callback: (state: LiveState) => void) =>
    Events.On('kspeech:live', (event) => callback(event.data as LiveState)),
  onNotification: (callback: (notification: AppNotification) => void) =>
    Events.On('kspeech:notification', (event) => callback(event.data as AppNotification)),
  onAssistantState: (callback: (state: AssistantState) => void) =>
    Events.On('kspeech:assistant', (event) => callback(event.data as AssistantState)),
}
