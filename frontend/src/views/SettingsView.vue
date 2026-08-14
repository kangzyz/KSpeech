<script setup lang="ts">
import { computed, nextTick, reactive, ref, watch } from 'vue'
import { api } from '../api'
import { captionColourApplies } from '../captionStyle'
import { formatDuration } from '../format'
import { speakerStyle } from '../speakers'
import AppIcon from '../components/AppIcon.vue'
import CaptionPreview from '../components/CaptionPreview.vue'
import ColorField from '../components/ColorField.vue'
import PluginFields from '../components/PluginFields.vue'
import SegmentedControl from '../components/SegmentedControl.vue'
import SettingsCard from '../components/SettingsCard.vue'
import ToggleSwitch from '../components/ToggleSwitch.vue'
import type { AppSnapshot, ConfigField, PluginOption } from '../types'

const props = defineProps<{ snapshot: AppSnapshot }>()

const tabs = [
  { id: 'general', label: '通用', icon: 'sliders' },
  { id: 'appearance', label: '外观', icon: 'palette' },
  { id: 'notification', label: '通知', icon: 'bell' },
  { id: 'audio', label: '音频源', icon: 'mic' },
  { id: 'recognizer', label: '识别引擎', icon: 'waveform' },
  { id: 'assistant', label: 'AI 助手', icon: 'sparkles' },
  { id: 'resources', label: '资源', icon: 'package' },
  { id: 'about', label: '关于', icon: 'info' },
]

// Endpoints and model names are typed by hand; these only save keystrokes.
const ENDPOINT_SUGGESTIONS = [
  'https://api.deepseek.com/v1',
  'https://dashscope.aliyuncs.com/compatible-mode/v1',
  'https://open.bigmodel.cn/api/paas/v4',
  'https://api.moonshot.cn/v1',
  'https://api.openai.com/v1',
  'https://openrouter.ai/api/v1',
  'http://127.0.0.1:11434/v1',
]

const MODEL_SUGGESTIONS = ['deepseek-chat', 'qwen-plus', 'glm-4-flash', 'moonshot-v1-8k', 'gpt-4o-mini']

// Settings a running job captured at start time and cannot re-read.
const RUN_LOCKED_TABS = ['notification', 'audio', 'recognizer']

const FONT_SUGGESTIONS = [
  'Microsoft YaHei UI',
  '微软雅黑',
  'Segoe UI Variable Text',
  'Segoe UI',
  'Arial',
  'SimHei',
  'SimSun',
  'Noto Sans SC',
  'Source Han Sans SC',
  'Consolas',
]

const activeTab = ref('general')
const content = ref<HTMLElement | null>(null)
const local = reactive<Record<string, unknown>>({ ...props.snapshot.config })

// Each category is its own page: keeping the previous scroll offset drops the
// user into the middle of a shorter one.
watch(activeTab, () => nextTick(() => content.value?.scrollTo({ top: 0 })))
const saveState = ref<'idle' | 'saving' | 'saved' | 'error'>('idle')
const actionError = ref('')
let savedTimer: number | undefined

watch(
  () => props.snapshot.config,
  (config) => Object.assign(local, config),
  { deep: true },
)

const running = computed(() => props.snapshot.status === 'running')
const runLocked = computed(() => running.value && RUN_LOCKED_TABS.includes(activeTab.value))
const activeLabel = computed(() => tabs.find((tab) => tab.id === activeTab.value)?.label ?? '')
const statusLabel = computed(() =>
  props.snapshot.status === 'running' ? '正在识别' : props.snapshot.status === 'paused' ? '已暂停' : '未运行',
)

// The backend decides which hosted tools an endpoint can take, so the settings
// page reports its answer instead of guessing from the address.
const assistantTools = computed(() => props.snapshot.assistant.tools ?? [])

const selectedRecognizer = computed(() =>
  props.snapshot.recognizers.find((item) => item.key === local['recognizer.source']),
)

const capturedInputs = computed(() => props.snapshot.audioSources.filter((item) => item.enabled))

// Punctuation models arrive through the resource page; typing a path stays
// possible for a model downloaded by hand.
const punctuationModels = computed(() => props.snapshot.punctuationModels ?? [])
const punctuationSelection = computed(() => {
  const current = text('punctuation.ModelPath')
  return punctuationModels.value.some((item) => String(item.value) === current) ? current : ''
})
const punctuationCustom = ref(false)
const showPunctuationPath = computed(() => punctuationCustom.value || punctuationSelection.value === '')

const choosePunctuationModel = async (value: string) => {
  // Picking "custom" only reveals the path field; it must not wipe the path
  // that is already configured.
  punctuationCustom.value = value === ''
  if (value !== '') await update('punctuation.ModelPath', value)
}

const number = (key: string, fallback: number) => Number(local[key] ?? fallback)
const text = (key: string, fallback = '') => String(local[key] ?? fallback)
const flag = (key: string, fallback = false) => Boolean(local[key] ?? fallback)

const markSaved = () => {
  saveState.value = 'saved'
  window.clearTimeout(savedTimer)
  savedTimer = window.setTimeout(() => (saveState.value = 'idle'), 1600)
}

const update = async (key: string, value: unknown) => {
  const previous = local[key]
  local[key] = value
  actionError.value = ''
  saveState.value = 'saving'
  window.clearTimeout(savedTimer)
  try {
    await api.setConfig(key, value)
    markSaved()
  } catch (error) {
    local[key] = previous
    saveState.value = 'error'
    actionError.value = error instanceof Error ? error.message : String(error)
  }
}

/**
 * Sends the whole list of captured audio inputs with one input changed. The
 * backend owns the list, so a rejected change simply leaves the snapshot as it
 * was instead of needing a local rollback.
 */
const saveAudioChannels = async (change: { key: string; enabled?: boolean; label?: string }) => {
  const channels = props.snapshot.audioSources
    .map((item) => {
      const target = item.key === change.key
      return {
        source: item.key,
        label: target && change.label !== undefined ? change.label : (item.label ?? ''),
        // An input this machine cannot open is dropped rather than resent:
        // keeping it would make every later change fail on its account.
        enabled:
          item.available && (target && change.enabled !== undefined ? change.enabled : Boolean(item.enabled)),
      }
    })
    .filter((item) => item.enabled)
    .map(({ source, label }) => ({ source, label }))

  actionError.value = ''
  saveState.value = 'saving'
  window.clearTimeout(savedTimer)
  try {
    await api.setAudioChannels(channels)
    markSaved()
  } catch (error) {
    saveState.value = 'error'
    actionError.value = error instanceof Error ? error.message : String(error)
  }
}

const updatePluginField = async (plugin: PluginOption | undefined, field: ConfigField, value: unknown) => {
  if (!plugin) return
  const previous = field.value
  field.value = value
  actionError.value = ''
  saveState.value = 'saving'
  window.clearTimeout(savedTimer)
  const values = Object.fromEntries((plugin.fields || []).map((item) => [item.key, item.value]))
  try {
    await api.setPluginConfig(plugin.key, values)
    markSaved()
  } catch (error) {
    field.value = previous
    saveState.value = 'error'
    actionError.value = error instanceof Error ? error.message : String(error)
  }
}

const run = async (operation: () => Promise<void>) => {
  actionError.value = ''
  try {
    await operation()
  } catch (error) {
    actionError.value = error instanceof Error ? error.message : String(error)
  }
}

const testing = ref(false)
const testResult = ref('')

const testAssistant = async () => {
  testing.value = true
  testResult.value = '正在请求模型…'
  actionError.value = ''
  try {
    const reply = await api.testAssistant()
    testResult.value = `连接成功，模型回复：${reply.slice(0, 60)}`
  } catch (error) {
    testResult.value = ''
    actionError.value = error instanceof Error ? error.message : String(error)
  } finally {
    testing.value = false
  }
}
</script>

<template>
  <section class="settings-layout">
    <aside class="settings-sidebar">
      <div class="sidebar-brand">
        <span class="brand-mark" aria-hidden="true" />
        <div>
          <strong>KSpeech</strong>
          <small>本地实时字幕</small>
        </div>
      </div>

      <nav class="settings-nav scroll-area" aria-label="设置分类">
        <button
          v-for="tab in tabs"
          :key="tab.id"
          type="button"
          :class="{ active: activeTab === tab.id }"
          :title="tab.label"
          @click="activeTab = tab.id"
        >
          <AppIcon :name="tab.icon" :size="17" /><span>{{ tab.label }}</span>
        </button>
      </nav>

      <div class="sidebar-footer">
        <div class="sidebar-status" :title="statusLabel">
          <span class="status-dot" :class="snapshot.status" />
          <span>{{ statusLabel }}</span>
          <span v-if="snapshot.status !== 'stopped'" class="run-clock">
            {{ formatDuration(snapshot.runningSeconds) }}
          </span>
        </div>
      </div>
    </aside>

    <div class="settings-main">
      <header class="settings-header">
        <div>
          <span class="eyebrow">设置</span>
          <h1>{{ activeLabel }}</h1>
        </div>
        <span class="save-indicator" :class="saveState" role="status">
          <AppIcon v-if="saveState === 'saved'" name="check" :size="15" />
          <AppIcon v-else-if="saveState === 'error'" name="alert" :size="15" />
          {{
            saveState === 'saving'
              ? '正在保存'
              : saveState === 'saved'
                ? '已保存'
                : saveState === 'error'
                  ? '保存失败'
                  : ''
          }}
        </span>
      </header>

      <div ref="content" class="settings-content scroll-area">
        <div v-if="runLocked" class="banner warn">
          <AppIcon name="alert" :size="16" />
          <span>正在识别，这一组设置需要停止后才能修改。</span>
          <button class="btn ghost" style="margin-left: auto" @click="run(api.stop)">停止识别</button>
        </div>

        <div v-if="actionError || snapshot.lastError" class="banner error" role="alert">
          <AppIcon name="alert" :size="16" />
          <span>{{ actionError || snapshot.lastError }}</span>
        </div>

        <!-- 通用 -->
        <template v-if="activeTab === 'general'">
          <SettingsCard title="启动行为" description="控制打开 KSpeech 之后是否立即开始识别。">
            <div class="switch-row">
              <div>
                <strong>启动后自动开始识别</strong>
                <small>需要识别引擎和模型已经配置就绪，否则启动会直接失败。</small>
              </div>
              <ToggleSwitch
                :model-value="flag('general.StartOnLaunch')"
                label="启动后自动开始识别"
                @update:model-value="update('general.StartOnLaunch', $event)"
              />
            </div>
          </SettingsCard>

          <SettingsCard title="识别记录" description="每次开始识别都会新建一个按时间命名的文本文件。">
            <label class="field">
              <span class="field-label">日志文件夹</span>
              <input
                class="input mono"
                type="text"
                spellcheck="false"
                :value="text('general.ResultLogPath')"
                placeholder="留空则不写入磁盘"
                @change="update('general.ResultLogPath', ($event.target as HTMLInputElement).value)"
              />
              <span class="field-hint">留空时本次运行的字幕仍会留在主窗口的实时字幕里，只是不落盘。</span>
            </label>
          </SettingsCard>

          <SettingsCard title="主窗口" description="位置和大小会自动记忆；被移出屏幕或调得太小时可以重置。">
            <div class="input-row">
              <button class="btn" @click="run(api.resetWindow)">
                <AppIcon name="reset" :size="16" />重置窗口位置
              </button>
              <button class="btn" @click="run(api.showConsole)">
                <AppIcon name="window" :size="16" />显示主窗口
              </button>
              <button class="btn" :disabled="!snapshot.locked" @click="run(() => api.setLocked(false))">
                <AppIcon name="unlock" :size="16" />解除锁定
              </button>
            </div>
            <p class="field-note">
              锁定只固定窗口的位置和大小，字幕照样能翻看、AI 照样能提问。窗口隐藏或锁定后，都可以从任务栏托盘图标恢复。
            </p>
          </SettingsCard>
        </template>

        <!-- 外观 -->
        <template v-else-if="activeTab === 'appearance'">
          <SettingsCard title="实时预览" description="下面的调整会立即应用到主窗口的实时字幕。">
            <CaptionPreview :config="local" />
          </SettingsCard>

          <SettingsCard title="字体">
            <div class="grid-2">
              <label class="field">
                <span class="field-label">字体</span>
                <input
                  class="input"
                  list="kspeech-fonts"
                  :value="text('appearance.FontFamily', 'Arial')"
                  @change="update('appearance.FontFamily', ($event.target as HTMLInputElement).value)"
                />
                <datalist id="kspeech-fonts">
                  <option v-for="font in FONT_SUGGESTIONS" :key="font" :value="font" />
                </datalist>
              </label>
              <label class="field">
                <span class="field-label">字号</span>
                <input
                  class="input"
                  type="number"
                  min="12"
                  max="160"
                  :value="number('appearance.FontSize', 48)"
                  @change="update('appearance.FontSize', Number(($event.target as HTMLInputElement).value))"
                />
                <span class="field-hint">12 – 160。字幕现在排在滚动消息框里，这个值会按比例缩放，上面的预览就是实际大小。</span>
              </label>
              <label class="field">
                <span class="field-label">阴影大小</span>
                <input
                  class="input"
                  type="number"
                  min="0"
                  max="40"
                  :value="number('appearance.ShadowSize', 10)"
                  @change="update('appearance.ShadowSize', Number(($event.target as HTMLInputElement).value))"
                />
                <span class="field-hint">0 表示关闭描边阴影。</span>
              </label>
              <div class="field">
                <span class="field-label">对齐方式</span>
                <SegmentedControl
                  label="对齐方式"
                  :model-value="number('appearance.TextAlign', 0)"
                  :options="[
                    { value: 0, label: '左' },
                    { value: 1, label: '中' },
                    { value: 2, label: '右' },
                    { value: 3, label: '两端' },
                  ]"
                  @update:model-value="update('appearance.TextAlign', $event)"
                />
              </div>
            </div>
          </SettingsCard>

          <SettingsCard title="颜色" description="滑块调整不透明度。背景色叠在窗口的毛玻璃面板上，全透明就是纯毛玻璃。">
            <div class="grid-2">
              <ColorField
                label="字体颜色"
                :model-value="number('appearance.FontColor', 0xffffffff)"
                @update:model-value="update('appearance.FontColor', $event)"
              />
              <ColorField
                label="阴影颜色"
                :model-value="number('appearance.ShadowColor', 0xff000000)"
                @update:model-value="update('appearance.ShadowColor', $event)"
              />
              <ColorField
                label="背景颜色"
                :model-value="number('appearance.BackgroundColor', 0)"
                @update:model-value="update('appearance.BackgroundColor', $event)"
              />
              <ColorField
                label="悬停背景"
                :model-value="number('appearance.MouseHover', 0x2709a9ff)"
                @update:model-value="update('appearance.MouseHover', $event)"
              />
            </div>
            <p v-if="!captionColourApplies(local)" class="field-note">
              当前的字体颜色在这个主题的毛玻璃面板上看不清，字幕暂时改用主题自带的颜色，阴影也一并省掉。换一个对比度够的颜色就会立刻按你选的画——出厂默认的白字是给旧版全透明字幕窗准备的，深色主题下仍然生效。
            </p>
          </SettingsCard>
        </template>

        <!-- 通知 -->
        <template v-else-if="activeTab === 'notification'">
          <SettingsCard title="通知方式" description="错误通知始终发送，这里只影响普通提醒。" :disabled="running">
            <div class="field">
              <span class="field-label">普通通知</span>
              <SegmentedControl
                label="普通通知"
                :disabled="running"
                :model-value="number('notification.NotificationType', 1)"
                :options="[
                  { value: 0, label: '关闭' },
                  { value: 1, label: '系统通知' },
                ]"
                @update:model-value="update('notification.NotificationType', $event)"
              />
            </div>
          </SettingsCard>

          <SettingsCard title="敏感词提醒" description="命中的句子每句最多提醒一次。" :disabled="running">
            <label class="field">
              <span class="field-label">敏感词列表</span>
              <textarea
                class="textarea"
                rows="7"
                :disabled="running"
                :value="text('notification.SensitiveWords')"
                placeholder="使用英文逗号、中文逗号或换行分隔"
                @change="update('notification.SensitiveWords', ($event.target as HTMLTextAreaElement).value)"
              />
              <span class="field-hint">例如：你的名字、项目代号，或者其他需要留意的关键词。</span>
            </label>
          </SettingsCard>
        </template>

        <!-- 音频源 -->
        <template v-else-if="activeTab === 'audio'">
          <SettingsCard
            title="音频输入"
            description="可以同时录多路：麦克风录你自己，系统声音录会议里的其他人。每一路的说话人名字会显示在字幕和识别历史里。"
            :disabled="running"
          >
            <div
              v-for="item in snapshot.audioSources"
              :key="item.key"
              class="audio-input"
              :class="{ captured: item.enabled }"
              :style="speakerStyle(snapshot, item.key)"
            >
              <div class="switch-row">
                <div>
                  <strong>
                    <span v-if="item.enabled" class="speaker-dot" aria-hidden="true" />{{ item.name }}
                  </strong>
                  <small>{{ item.description }}</small>
                </div>
                <ToggleSwitch
                  :model-value="Boolean(item.enabled)"
                  :disabled="running || !item.available || (item.enabled && capturedInputs.length < 2)"
                  :label="`录制${item.name}`"
                  @update:model-value="saveAudioChannels({ key: item.key, enabled: $event })"
                />
              </div>

              <p v-if="!item.available" class="field-note">这台电脑上暂时不可用，无法录制。</p>

              <template v-else-if="item.enabled">
                <label class="field">
                  <span class="field-label">说话人名字</span>
                  <input
                    class="input"
                    type="text"
                    maxlength="12"
                    :disabled="running"
                    :value="item.label"
                    placeholder="留空则字幕上不加前缀"
                    @change="saveAudioChannels({ key: item.key, label: ($event.target as HTMLInputElement).value })"
                  />
                  <span class="field-hint">
                    最多 12 个字，例如「我」「其他人」「客户」。同时录多路时留空会自动补回默认名字，否则两行分不出是谁。
                  </span>
                </label>

                <PluginFields
                  v-if="item.fields?.length"
                  :fields="item.fields"
                  :disabled="running"
                  @change="(field, value) => updatePluginField(item, field, value)"
                />
              </template>
            </div>
          </SettingsCard>

          <div v-if="capturedInputs.length > 1" class="banner warn">
            <AppIcon name="alert" :size="16" />
            <span>
              每多录一路，就会多跑一份识别引擎：内存和 CPU 大致按路数翻倍。建议戴耳机，否则麦克风会把对方的声音再录一遍，同一句话会记成两个人说的。
            </span>
          </div>

          <div v-if="selectedRecognizer && selectedRecognizer.needsAudio === false" class="banner info">
            <AppIcon name="info" :size="16" />
            <span>当前识别引擎自行采集音频，这里的音频输入设置不会生效。</span>
          </div>
        </template>

        <!-- 识别引擎 -->
        <template v-else-if="activeTab === 'recognizer'">
          <SettingsCard
            title="识别引擎"
            description="本地模型不会把语音发送到网络；外部命令由你配置的程序负责。"
            :disabled="running"
          >
            <label class="field">
              <span class="field-label">引擎</span>
              <select
                class="select"
                :disabled="running"
                :value="text('recognizer.source')"
                @change="update('recognizer.source', ($event.target as HTMLSelectElement).value)"
              >
                <option
                  v-for="item in snapshot.recognizers"
                  :key="item.key"
                  :value="item.key"
                  :disabled="!item.available"
                >
                  {{ item.name }}{{ item.available ? '' : '（当前不可用）' }}
                </option>
              </select>
              <span v-if="selectedRecognizer?.description" class="field-hint">
                {{ selectedRecognizer.description }}
              </span>
            </label>

            <PluginFields
              v-if="selectedRecognizer?.fields?.length"
              :fields="selectedRecognizer.fields"
              :disabled="running"
              @change="(field, value) => updatePluginField(selectedRecognizer, field, value)"
            />
          </SettingsCard>

          <SettingsCard
            title="标点符号"
            description="流式识别模型只输出文字，标点由 KSpeech 在整句结束后补上，识别中的临时字幕保持原样。"
            :disabled="running"
          >
            <div class="field">
              <span class="field-label">标点方式</span>
              <SegmentedControl
                label="标点方式"
                :disabled="running"
                :model-value="text('punctuation.Mode', 'rules')"
                :options="[
                  { value: 'off', label: '关闭' },
                  { value: 'rules', label: '基础规则' },
                  { value: 'model', label: '标点模型' },
                ]"
                @update:model-value="update('punctuation.Mode', $event)"
              />
              <span class="field-hint">
                基础规则只在句末补句号或问号，不需要额外下载；标点模型还会在句子中间断句加逗号，需要多占一份内存和一点 CPU。
              </span>
            </div>

            <template v-if="text('punctuation.Mode', 'rules') === 'model'">
              <div class="field">
                <span class="field-label">标点模型</span>
                <select
                  class="select"
                  :disabled="running"
                  :value="punctuationCustom ? '' : punctuationSelection"
                  @change="choosePunctuationModel(($event.target as HTMLSelectElement).value)"
                >
                  <option value="">自定义文件路径</option>
                  <option
                    v-for="item in punctuationModels"
                    :key="String(item.value)"
                    :value="String(item.value)"
                  >
                    {{ item.label }}
                  </option>
                </select>
                <span v-if="punctuationModels.length === 0" class="field-hint">
                  还没有安装标点模型。到「资源」页安装「中英标点模型」（62 MB），装完这里就能直接选，不用手填路径。
                </span>
              </div>

              <label v-if="showPunctuationPath" class="field">
                <span class="field-label">标点模型文件</span>
                <span class="input-row">
                  <input
                    class="input mono"
                    type="text"
                    spellcheck="false"
                    :disabled="running"
                    :value="text('punctuation.ModelPath')"
                    placeholder="文件绝对路径，例如 D:\models\punct\model.onnx"
                    @change="update('punctuation.ModelPath', ($event.target as HTMLInputElement).value)"
                  />
                  <AppIcon name="file" :size="17" />
                </span>
                <span class="field-hint">
                  指向 sherpa-onnx 中英标点模型解压后的 model.onnx（或 int8 版的 model.int8.onnx）。只有带原生 sherpa-onnx
                  后端的发布构建能加载；模型读不出来时会自动退回基础规则并提示原因。
                </span>
              </label>
            </template>
          </SettingsCard>
        </template>

        <!-- AI 助手 -->
        <template v-else-if="activeTab === 'assistant'">
          <div class="banner warn">
            <AppIcon name="alert" :size="16" />
            <span>
              开启后，已完成的字幕会发送到你在下面填写的模型接口，用于生成要点和回答；关闭时 KSpeech 完全离线。
            </span>
          </div>

          <SettingsCard title="AI 助手" description="用你自己的模型接口做实时关键内容汇总，以及对提问的实时回复。">
            <div class="switch-row">
              <div>
                <strong>开启 AI 助手</strong>
                <small>需要先填好下面的 API 地址和模型名称，建议先点“测试连接”。</small>
              </div>
              <ToggleSwitch
                :model-value="flag('assistant.Enabled')"
                label="开启 AI 助手"
                @update:model-value="update('assistant.Enabled', $event)"
              />
            </div>
            <p class="field-note">
              要点和问答都在主窗口里：中间一栏是要点总结，右边一栏是 AI 对话，都可以从标题栏的按钮收起。
            </p>
          </SettingsCard>

          <SettingsCard
            title="模型接口"
            description="任何兼容 OpenAI /chat/completions 的服务都可以：DeepSeek、通义千问、智谱、Moonshot、OpenRouter，或本机的 Ollama、LM Studio、vLLM。"
          >
            <label class="field">
              <span class="field-label">API 地址</span>
              <input
                class="input mono"
                type="text"
                spellcheck="false"
                list="kspeech-endpoints"
                placeholder="https://api.deepseek.com/v1"
                :value="text('assistant.Endpoint')"
                @change="update('assistant.Endpoint', ($event.target as HTMLInputElement).value)"
              />
              <datalist id="kspeech-endpoints">
                <option v-for="endpoint in ENDPOINT_SUGGESTIONS" :key="endpoint" :value="endpoint" />
              </datalist>
              <span class="field-hint">
                填到 /v1 为止即可，KSpeech 会自动补上 /chat/completions。公网地址必须用 https，本机和内网可以用 http。
              </span>
            </label>

            <label class="field">
              <span class="field-label">API Key</span>
              <input
                class="input mono"
                type="password"
                autocomplete="off"
                spellcheck="false"
                placeholder="本机模型可以留空"
                :value="text('assistant.ApiKey')"
                @change="update('assistant.ApiKey', ($event.target as HTMLInputElement).value)"
              />
              <span class="field-hint">
                以明文保存在 %APPDATA%\KSpeech\config.json，建议使用额度受限的专用 Key。
              </span>
            </label>

            <label class="field">
              <span class="field-label">模型名称</span>
              <input
                class="input"
                type="text"
                spellcheck="false"
                list="kspeech-models"
                placeholder="deepseek-chat"
                :value="text('assistant.Model')"
                @change="update('assistant.Model', ($event.target as HTMLInputElement).value)"
              />
              <datalist id="kspeech-models">
                <option v-for="model in MODEL_SUGGESTIONS" :key="model" :value="model" />
              </datalist>
              <span class="field-hint">用便宜的小模型就够：要点和回答都是短文本。</span>
            </label>

            <div class="input-row">
              <button class="btn" :disabled="testing" @click="testAssistant">
                <AppIcon name="refresh" :size="16" />测试连接
              </button>
              <span v-if="testResult" class="result-count">{{ testResult }}</span>
            </div>
          </SettingsCard>

          <SettingsCard title="实时关键内容汇总" description="按间隔把这段时间说过的话浓缩成要点，显示在 AI 助手窗口。">
            <div class="switch-row">
              <div>
                <strong>汇总关键要点</strong>
                <small>只发送已完成的整句，识别中的临时结果不会外发。</small>
              </div>
              <ToggleSwitch
                :model-value="flag('assistant.Summarize')"
                label="汇总关键要点"
                @update:model-value="update('assistant.Summarize', $event)"
              />
            </div>
            <label class="field">
              <span class="field-label">汇总间隔（秒）</span>
              <input
                class="input"
                type="number"
                min="15"
                max="1800"
                :value="number('assistant.SummaryIntervalSeconds', 90)"
                @change="update('assistant.SummaryIntervalSeconds', Number(($event.target as HTMLInputElement).value))"
              />
              <span class="field-hint">15 – 1800 秒。间隔越短越及时，消耗的 token 也越多；停止识别时会补一次收尾汇总。</span>
            </label>
          </SettingsCard>

          <SettingsCard
            title="模型内置工具"
            description="声明模型服务商自己托管的工具，最主要的是联网搜索。工具由服务商执行并计费，KSpeech 只负责声明和读取结果。"
          >
            <div class="switch-row">
              <div>
                <strong>使用模型自带的联网搜索</strong>
                <small>回答提问时声明；生成关键要点不会带上，避免为已经说过的内容付搜索费用。</small>
              </div>
              <ToggleSwitch
                :model-value="flag('assistant.Tools')"
                label="使用模型自带的联网搜索"
                @update:model-value="update('assistant.Tools', $event)"
              />
            </div>
            <div v-if="flag('assistant.Tools')" class="field">
              <span class="field-label">当前接口</span>
              <p class="field-note">
                <template v-if="assistantTools.length">
                  已识别 {{ snapshot.assistant.provider }}，将声明：{{ assistantTools.join('、') }}。
                </template>
                <template v-else-if="snapshot.assistant.provider">
                  已识别 {{ snapshot.assistant.provider }}，但它在 /chat/completions 上没有可声明的托管工具。
                </template>
                <template v-else>根据 API 地址和模型名称判断服务商，填好上面两项后这里会显示结果。</template>
              </p>
              <span v-if="snapshot.assistant.toolNote" class="field-hint">{{ snapshot.assistant.toolNote }}</span>
              <span class="field-hint">
                DeepSeek、豆包、xAI、Claude 的官方 /chat/completions 不提供托管工具；接口若拒绝工具声明，KSpeech 会自动去掉工具重试一次，回答照常返回。
              </span>
            </div>
          </SettingsCard>

          <SettingsCard title="实时问答" description="字幕里出现问句时自动作答；也可以在 AI 助手窗口手动追问。">
            <div class="switch-row">
              <div>
                <strong>自动回答识别到的问题</strong>
                <small>命中问句后带上最近的上下文请求模型，两次自动回答之间至少间隔 8 秒。</small>
              </div>
              <ToggleSwitch
                :model-value="flag('assistant.AutoAnswer')"
                label="自动回答识别到的问题"
                @update:model-value="update('assistant.AutoAnswer', $event)"
              />
            </div>
            <label class="field">
              <span class="field-label">上下文句数</span>
              <input
                class="input"
                type="number"
                min="4"
                max="200"
                :value="number('assistant.ContextSentences', 30)"
                @change="update('assistant.ContextSentences', Number(($event.target as HTMLInputElement).value))"
              />
              <span class="field-hint">回答问题时带上最近多少句字幕，4 – 200 句。</span>
            </label>
          </SettingsCard>

          <SettingsCard title="高级">
            <label class="field">
              <span class="field-label">补充背景</span>
              <textarea
                class="textarea"
                rows="5"
                :value="text('assistant.Background')"
                placeholder="例如：这是 KSpeech 项目的周会，参会人有产品、后端、测试三方；专有名词写作 Wails、sherpa-onnx。"
                @change="update('assistant.Background', ($event.target as HTMLTextAreaElement).value)"
              />
              <span class="field-hint">会附加到每次请求里，用来纠正专有名词、说明会议主题。最多 4000 字。</span>
            </label>
            <label class="field">
              <span class="field-label">请求超时（秒）</span>
              <input
                class="input"
                type="number"
                min="5"
                max="180"
                :value="number('assistant.TimeoutSeconds', 30)"
                @change="update('assistant.TimeoutSeconds', Number(($event.target as HTMLInputElement).value))"
              />
              <span class="field-hint">5 – 180 秒。本机大模型推理慢时可以调大。</span>
            </label>
          </SettingsCard>
        </template>

        <!-- 资源 -->
        <template v-else-if="activeTab === 'resources'">
          <SettingsCard title="模型与扩展" description="内置资源不可移除；用户资源安装在应用数据目录。">
            <template #action>
              <button class="btn" @click="run(api.refreshResources)">
                <AppIcon name="refresh" :size="16" />刷新
              </button>
            </template>

            <div v-if="snapshot.resources.length === 0" class="empty-state">
              <AppIcon name="package" :size="26" />
              <strong>没有可用资源</strong>
              <span>检查网络连接后点击刷新。</span>
            </div>

            <div v-for="resource in snapshot.resources" :key="resource.id" class="resource-row">
              <div class="resource-info">
                <strong>{{ resource.name }}</strong>
                <span>{{ resource.description || resource.id }}</span>
                <span class="resource-meta">
                  <span>版本 {{ resource.displayVersion || '未知' }}</span>
                  <span v-if="resource.local && !resource.removable" class="tag installed">内置</span>
                  <span v-else-if="resource.local" class="tag installed">已安装</span>
                  <span v-if="resource.needsUpdate" class="tag update">可更新</span>
                  <span v-if="!resource.installable" class="tag blocked">不兼容</span>
                </span>
              </div>

              <div class="resource-actions">
                <span v-if="resource.error" class="result-count" style="color: var(--danger)">
                  {{ resource.error }}
                </span>
                <span v-if="resource.busy" class="progress" role="progressbar">
                  <span :style="{ width: `${Math.round((resource.progress || 0) * 100)}%` }" />
                </span>
                <button
                  v-if="resource.installable && (!resource.local || resource.needsUpdate)"
                  class="btn"
                  :disabled="resource.busy"
                  @click="run(() => api.installResource(resource.id))"
                >
                  <AppIcon name="download" :size="16" />{{ resource.needsUpdate ? '更新' : '安装' }}
                </button>
                <button
                  v-if="resource.local && resource.removable"
                  class="icon-btn danger"
                  title="移除"
                  aria-label="移除"
                  :disabled="resource.busy"
                  @click="run(() => api.removeResource(resource.id))"
                >
                  <AppIcon name="trash" :size="17" />
                </button>
              </div>
            </div>
          </SettingsCard>
        </template>

        <!-- 关于 -->
        <template v-else>
          <SettingsCard title="关于 KSpeech">
            <div class="about-hero">
              <span class="brand-mark large" aria-hidden="true" />
              <div>
                <h2>KSpeech {{ snapshot.version }}</h2>
                <p>Windows 本地实时语音字幕，基于 Go + Wails 3 + Vue 3 构建。</p>
              </div>
            </div>
            <dl class="about-list">
              <div><dt>版本</dt><dd>{{ snapshot.version }}</dd></div>
              <div><dt>提交</dt><dd>{{ snapshot.commit || '开发构建' }}</dd></div>
              <div><dt>平台</dt><dd>{{ snapshot.platform }}</dd></div>
              <div><dt>配置文件</dt><dd>%APPDATA%\KSpeech\config.json</dd></div>
              <div><dt>识别日志</dt><dd>{{ text('general.ResultLogPath') || '未启用' }}</dd></div>
            </dl>
          </SettingsCard>
        </template>
      </div>
    </div>
  </section>
</template>
