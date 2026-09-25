<template>
  <div class="app-stage">
    <div class="paper-grain" aria-hidden="true"></div>

    <header class="navbar">
      <div class="nav-content">
        <div class="brand" aria-label="Vparser">
          <svg class="brand-mark" width="26" height="26" viewBox="0 0 32 32" aria-hidden="true">
            <circle cx="16" cy="16" r="14.5" fill="none" stroke="currentColor" stroke-width="1.4" />
            <path d="M4.5 20.5 L11 13.5 L15 17.5 L20 11 L27.5 20.5" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round" />
            <path d="M3.2 22.6 H28.8" stroke="currentColor" stroke-width="1.4" />
          </svg>
          <span class="brand-name">Vparser</span>
        </div>

        <div class="nav-controls">
          <div class="status-pill" :class="{ 'is-active': uploading || activeTasks.length }" role="status" aria-live="polite">
            <span class="status-dot"></span>
            <span class="status-text">{{ systemStatusText }}</span>
          </div>

          <div class="lang-switch" role="group" :aria-label="t('nav.language')">
            <button
                v-for="option in LOCALES"
                :key="option.value"
                type="button"
                :class="{ active: locale === option.value }"
                :aria-pressed="locale === option.value"
                @click="setLocale(option.value)"
            >{{ option.label }}</button>
          </div>

          <button v-if="!currentUser" class="auth-btn" @click="openAuthModal">{{ t('nav.login') }}</button>

          <div v-else class="user-profile">
            <span class="user-avatar" aria-hidden="true">{{ (currentUser.nickname || currentUser.username || '?').slice(0, 1) }}</span>
            <span class="user-name">{{ currentUser.nickname }}</span>
            <button class="logout-btn" @click="logout" :title="t('nav.logout')" :aria-label="t('nav.logout')">
              <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4"></path><polyline points="16 17 21 12 16 7"></polyline><line x1="21" y1="12" x2="9" y2="12"></line></svg>
            </button>
          </div>
        </div>
      </div>
    </header>

    <main class="main-container">
      <section class="hero-section">
        <div class="hero-copy">
          <h1 class="slogan-main" v-html="t('hero.title')"></h1>
          <p class="slogan-sub">{{ t('hero.sub') }}</p>
        </div>
        <ol class="hero-steps">
          <li><strong>{{ t('hero.step1') }}</strong><span>{{ t('hero.step1.sub') }}</span></li>
          <li><strong>{{ t('hero.step2') }}</strong><span>{{ t('hero.step2.sub') }}</span></li>
          <li><strong>{{ t('hero.step3') }}</strong><span>{{ t('hero.step3.sub') }}</span></li>
        </ol>

        <svg class="horizon" viewBox="0 0 1200 120" preserveAspectRatio="none" aria-hidden="true">
          <path class="ridge far" d="M0 92 L90 70 L150 80 L240 46 L320 74 L400 58 L470 66 L560 30 L650 64 L720 52 L800 70 L880 40 L960 62 L1040 50 L1120 72 L1200 60 V120 H0 Z" />
          <path class="ridge near" d="M0 104 L120 88 L210 98 L300 80 L380 94 L470 86 L560 100 L650 84 L760 96 L850 82 L940 98 L1040 88 L1120 100 L1200 92 V120 H0 Z" />
          <line class="waterline" x1="0" y1="112" x2="1200" y2="112" />
        </svg>

        <div class="upload-wrapper">
          <input
              type="file"
              id="file-input"
              @change="handleFileChange"
              accept="video/*"
              hidden
          />

          <div
              class="upload-magnet"
              :class="{ 'processing': uploading, 'is-dragover': isDragOver }"
              @dragenter.prevent="handleDragEnter"
              @dragover.prevent="isDragOver = true"
              @dragleave.prevent="handleDragLeave"
              @drop.prevent="handleDrop"
          >
            <div class="split-container" v-if="!uploading">
              <label for="file-input" class="pane pane-local">
                <span class="pane-icon" aria-hidden="true">
                  <svg width="30" height="30" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"></path><polyline points="17 8 12 3 7 8"></polyline><line x1="12" y1="3" x2="12" y2="15"></line></svg>
                </span>
                <span class="pane-title">{{ isDragOver ? t('upload.drop') : t('upload.local') }}</span>
                <span class="pane-desc">{{ t('upload.local.desc') }}</span>
              </label>

              <div class="pane pane-url">
                <span class="pane-icon" aria-hidden="true">
                  <svg width="30" height="30" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"><path d="M10 13a5 5 0 0 0 7.54.54l3-3a5 5 0 0 0-7.07-7.07l-1.72 1.71"></path><path d="M14 11a5 5 0 0 0-7.54-.54l-3 3a5 5 0 0 0 7.07 7.07l1.71-1.71"></path></svg>
                </span>
                <span class="pane-title">{{ t('upload.url') }}</span>
                <span class="pane-desc">{{ t('upload.url.desc') }}</span>
                <div class="url-input-box" @click.stop>
                  <input
                      v-model="videoUrl"
                      type="text"
                      inputmode="url"
                      autocomplete="off"
                      spellcheck="false"
                      placeholder="https://"
                      :aria-label="t('upload.url.aria')"
                      :disabled="uploading"
                      @keyup.enter="handleUrlUpload"
                  />
                  <button class="url-go-btn" :disabled="uploading || !videoUrl.trim()" @click="handleUrlUpload">{{ t('upload.url.go') }}</button>
                </div>
              </div>
            </div>

            <div class="magnet-content busy" v-else>
              <span class="busy-text">{{ uploadProgress.label }}</span>
              <span v-if="uploadProgress.filename" class="busy-file">{{ uploadProgress.filename }}</span>
              <div
                  v-if="uploadProgress.percent !== null"
                  class="upload-progress"
                  role="progressbar"
                  :aria-label="t('upload.progress.aria')"
                  aria-valuemin="0"
                  aria-valuemax="100"
                  :aria-valuenow="uploadProgress.percent"
              >
                <span :style="{ width: `${uploadProgress.percent}%` }"></span>
              </div>
              <div v-else class="upload-progress indeterminate" aria-hidden="true"><span></span></div>
              <span v-if="uploadProgress.detail" class="busy-stat" aria-live="polite">{{ uploadProgress.detail }}</span>
              <span v-if="uploadProgress.warning" class="busy-warning" role="status">{{ uploadProgress.warning }}</span>
              <div v-if="uploadAbort" class="busy-actions">
                <button type="button" @click="cancelUpload">{{ t('upload.cancel') }}</button>
              </div>
            </div>
          </div>

          <div v-if="resumableFile && !uploading" class="upload-resume" role="status">
            <span>{{ resumeHint }}</span>
            <button type="button" @click="resumeUpload">{{ t('upload.resume') }}</button>
            <button type="button" @click="discardResumableUpload">{{ t('upload.restart') }}</button>
          </div>
        </div>
        <transition name="toast-pop">
          <div
              v-if="message"
              class="notification-bar"
              :class="{ 'error': messageIsError }"
              :role="messageIsError ? 'alert' : 'status'"
              :aria-live="messageIsError ? 'assertive' : 'polite'"
              :title="messageIsError ? t('msg.dismiss') : null"
              @click="dismissMessage"
          >
            {{ message }}
          </div>
        </transition>
      </section>

      <section v-if="list.length > 0" class="workspace-section">
        <div class="section-header">
          <div class="library-title">
            <h2>{{ t('lib.title') }}</h2>
            <span class="count-chip">{{ list.length }}</span>
          </div>
          <label class="library-search">
            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round"><circle cx="11" cy="11" r="7.5"></circle><line x1="21" y1="21" x2="16.5" y2="16.5"></line></svg>
            <input v-model="searchQuery" type="search" :placeholder="t('lib.search')" :aria-label="t('lib.search.aria')" />
          </label>
        </div>
        <ul class="card-grid">
          <li v-for="item in visibleList" :key="item.id" class="project-card">
            <div class="card-meta">
              <span class="meta-icon" aria-hidden="true">
                <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round"><polygon points="23 7 16 12 23 17 23 7"></polygon><rect x="1" y="5" width="15" height="14" rx="2" ry="2"></rect></svg>
              </span>
              <div class="meta-info">
                <div class="filename-mask" :title="item.filename">{{ item.filename }}</div>
                <div class="meta-tags">
                  <span class="time-tag">{{ formatTime(item.uploadTime) }}</span>
                  <span
                      class="status-indicator"
                      :class="cardStatusClass(item)"
                      :title="cardStatusTitle(item)"
                  >{{ cardStatusLabel(item) }}</span>
                </div>
              </div>
            </div>

            <div class="action-dock">
              <button
                  class="dock-item ai-core"
                  :disabled="item.status !== 'COMPLETED'"
                  :title="actionTitle(item, t('lib.analyse.title'))"
                  @click="openAgent(item)"
              >{{ t('lib.analyse') }}</button>
              <button
                  class="dock-item"
                  :disabled="item.status !== 'COMPLETED'"
                  :title="actionTitle(item, t('lib.transcribe.title'))"
                  @click="transcribe(item.id)"
              >{{ t('lib.transcribe') }}</button>
              <button
                  class="dock-item"
                  :disabled="item.status !== 'COMPLETED'"
                  :title="actionTitle(item, t('lib.audio.title'))"
                  @click="downloadAudio(item)"
              >{{ t('lib.audio') }}</button>
              <button
                  class="delete-btn"
                  :disabled="deletingId === item.id"
                  :title="deletingId === item.id ? t('lib.deleting') : t('lib.delete')"
                  :aria-label="t('lib.delete.aria', { name: item.filename })"
                  @click.stop="deleteItem(item)"
              >
                <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round">
                  <polyline points="3 6 5 6 21 6"></polyline><path d="M19 6l-1 14H6L5 6"></path><path d="M8 6V4h8v2"></path>
                </svg>
              </button>
            </div>
          </li>
        </ul>
        <div v-if="visibleList.length === 0" class="library-empty">
          <p>{{ t('lib.noMatch', { query: searchQuery }) }}</p>
          <button type="button" @click="searchQuery = ''">{{ t('lib.clear') }}</button>
        </div>
      </section>
      <section v-else-if="currentUser" class="workspace-section empty-library">
        <p v-html="t('lib.empty')"></p>
      </section>

      <div class="sidebar-backdrop" v-if="sidebar.visible" @click="closeSidebar"></div>
      <div
          ref="sidebarPanel"
          class="sidebar-panel"
          :class="{ 'is-open': sidebar.visible }"
          :inert="!sidebar.visible"
          role="dialog"
          aria-modal="true"
          tabindex="-1"
          :aria-label="sidebar.title || t('side.details')"
      >
        <div class="sidebar-header">
          <div class="sidebar-title">
            <span class="icon" v-if="sidebar.type === 'ai'">
              <svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M2 12h2"></path><path d="M20 12h2"></path><path d="M12 2v2"></path><path d="M12 20v2"></path><path d="M20.2 6.47l-1.4 1.4"></path><path d="M15.9 5.35l-1.4-1.4"></path><path d="M9 11a3 3 0 1 0 6 0a3 3 0 0 0-6 0"></path></svg>
            </span>
            <span class="icon" v-else>
              <svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"></path><polyline points="14 2 14 8 20 8"></polyline><line x1="16" y1="13" x2="8" y2="13"></line><line x1="16" y1="17" x2="8" y2="17"></line><polyline points="10 9 9 9 8 9"></polyline></svg>
            </span>
            {{ sidebar.title }}
          </div>
          <button class="close-btn" @click="closeSidebar" :aria-label="t('side.close')">×</button>
        </div>
        <div ref="sidebarBody" class="sidebar-body">
          <div v-if="sidebar.type === 'ai'" class="video-evidence">
            <video
                v-if="sidebar.playbackUrl"
                ref="videoPlayer"
                :src="sidebar.playbackUrl"
                controls
                playsinline
                preload="metadata"
                @error="handlePlaybackError"
            ></video>
            <div v-else-if="sidebar.playbackLoading" class="video-evidence-loading">{{ t('side.videoLoading') }}</div>
            <div v-else-if="sidebar.playbackError" class="video-evidence-error" role="alert">
              <span>{{ sidebar.playbackError }}</span>
              <button type="button" @click="retryPlayback">{{ t('side.reload') }}</button>
            </div>
            <p v-if="sidebar.playbackUrl">{{ t('side.seekHint') }}</p>
          </div>
          <div v-if="sidebar.type === 'ai' && sidebar.mode === 'compose'" class="agent-composer">
            <p class="agent-caption">{{ t('side.mode') }}</p>
            <div class="goal-presets agent-mode-row">
              <button
                  v-for="m in analysisModes"
                  :key="m.value"
                  :class="{ active: sidebar.analysisMode === m.value }"
                  @click="sidebar.analysisMode = m.value"
              >
                <strong>{{ m.title }}</strong>
                <span>{{ m.description }}</span>
              </button>
            </div>
            <p class="agent-caption">{{ t('side.goal') }}</p>
            <p v-if="sidebar.error" class="inline-error" role="alert">{{ sidebar.error }}</p>
            <textarea
                v-model="sidebar.goal"
                maxlength="500"
                :placeholder="t('side.goal.placeholder')"
                @keydown.ctrl.enter.prevent="submitAgent"
                @keydown.meta.enter.prevent="submitAgent"
            ></textarea>
            <p v-if="sidebar.goal.length > 400" class="field-counter">{{ t('side.counter', { count: sidebar.goal.length }) }}</p>
            <div class="goal-presets">
              <button
                  v-for="preset in goalPresets"
                  :key="preset.title"
                  :class="{ active: sidebar.goal === preset.prompt }"
                  @click="sidebar.goal = preset.prompt"
              >
                <strong>{{ preset.title }}</strong>
                <span>{{ preset.description }}</span>
              </button>
            </div>
            <button class="agent-run-btn" :disabled="!sidebar.goal.trim()" @click="submitAgent">
              {{ sidebar.error ? t('side.rerun') : t('side.run') }}
            </button>
          </div>

          <div v-else-if="sidebar.loading" class="agent-running">
            <div class="loading-state">
              <div class="loader" aria-hidden="true"></div>
              <p aria-live="polite">{{ loadingHeadline }}</p>
              <p v-if="sidebar.streamOffline" class="stream-offline" role="status">
                {{ t('side.reconnecting', { count: sidebar.streamRetry }) }}
              </p>
              <p class="loading-hint">{{ t('side.background') }}</p>
            </div>
            <div v-if="sidebar.plan?.tasks?.length" class="agent-meta-block">
              <span class="meta-label">{{ t('side.plan') }}</span>
              <ol><li v-for="task in sidebar.plan.tasks" :key="task">{{ task }}</li></ol>
            </div>
            <div v-if="traceStages.length" class="agent-meta-block">
              <span class="meta-label">{{ t('side.stagesDone') }}</span>
              <div class="stage-list"><span v-for="stage in traceStages" :key="stage[0]">{{ stage[0] }} · {{ stage[1] }}</span></div>
            </div>
          </div>

          <div v-else>
            <div v-if="sidebar.type === 'ai'">
              <div class="result-actions">
                <button type="button" @click="startNewAnalysis">{{ t('side.newResult') }}</button>
                <button type="button" :disabled="!sidebar.content" @click="copyResult">{{ t('side.copy') }}</button>
                <button type="button" :disabled="!sidebar.content" @click="downloadResult">{{ t('side.export') }}</button>
              </div>
              <div class="evidence-search">
                <div class="evidence-search-form">
                  <input
                      v-model="sidebar.evidenceQuery"
                      :aria-label="t('side.evidence.aria')"
                      maxlength="500"
                      :placeholder="t('side.evidence.placeholder')"
                      @keyup.enter="searchEvidence"
                  />
                  <button type="button" :disabled="sidebar.evidenceLoading || !sidebar.evidenceQuery.trim()" @click="searchEvidence">
                    {{ sidebar.evidenceLoading ? t('side.evidence.searching') : t('side.evidence.go') }}
                  </button>
                </div>
                <p v-if="sidebar.evidenceError" class="evidence-search-error" aria-live="polite">{{ sidebar.evidenceError }}</p>
                <div v-if="sidebar.evidenceResults.length" class="evidence-search-results" aria-live="polite">
                  <button
                      v-for="hit in sidebar.evidenceResults"
                      :key="`${hit.startMs}-${hit.endMs}`"
                      type="button"
                      :title="hit.snippet || t('side.evidence.empty')"
                      @click="seekToEvidence(hit.startMs)"
                  >
                    <strong>{{ formatEvidenceTime(hit.startMs) }}</strong>
                    <small>{{ hit.source || t('side.evidence.source') }}</small>
                    <span>{{ hit.snippet || t('side.evidence.empty') }}</span>
                  </button>
                </div>
              </div>
              <div class="markdown-content" v-html="renderedMarkdown" @click="seekEvidence"></div>
              <details v-if="sidebar.plan?.tasks?.length || traceStages.length" class="agent-inspector">
                <summary>{{ t('side.inspector') }}</summary>
                <div class="agent-inspector-content">
                <div v-if="sidebar.plan?.tasks?.length" class="agent-meta-block">
                  <span class="meta-label">{{ t('side.plannerTasks') }}</span>
                  <div v-if="sidebar.editingPlan" class="plan-editor">
                    <div v-for="(_, index) in sidebar.planDraft" :key="index" class="plan-editor-row">
                      <input v-model="sidebar.planDraft[index]" maxlength="500" :aria-label="t('side.task', { n: index + 1 })" />
                      <button type="button" :title="t('side.removeTask')" @click="removePlanTask(index)">×</button>
                    </div>
                    <button v-if="sidebar.planDraft.length < 5" type="button" @click="addPlanTask">{{ t('side.addTask') }}</button>
                    <div class="plan-editor-actions">
                      <button type="button" @click="cancelPlanEdit">{{ t('side.cancel') }}</button>
                      <button type="button" :disabled="sidebar.rerunLoading" @click="rerunWithPlan">
                        {{ sidebar.rerunLoading ? t('side.submitting') : t('side.rerunPlan') }}
                      </button>
                    </div>
                  </div>
                  <template v-else>
                    <ol><li v-for="task in sidebar.plan.tasks" :key="task">{{ task }}</li></ol>
                    <button type="button" class="plan-edit-trigger" @click="startPlanEdit">{{ t('side.editPlan') }}</button>
                  </template>
                </div>
                <div v-if="traceStages.length" class="agent-meta-block">
                  <span class="meta-label">{{ t('side.trace') }}</span>
                  <div class="stage-list"><span v-for="stage in traceStages" :key="stage[0]">{{ stage[0] }} · {{ stage[1] }}</span></div>
                </div>
                <div v-if="sidebar.evaluation && Object.keys(sidebar.evaluation).length" class="quality-row">
                  <span>{{ t('side.quality.structure', { state: sidebar.evaluation.structuredValid ? t('side.quality.pass') : t('side.quality.todo') }) }}</span>
                  <span>{{ t('side.quality.evidence', { rate: formatPercent(sidebar.evaluation.evidenceSupportRate) }) }}</span>
                  <span>{{ t('side.quality.critic', { state: sidebar.evaluation.criticPassed ? t('side.quality.pass') : t('side.quality.limit') }) }}</span>
                </div>
                </div>
              </details>
              <div class="follow-up-box">
                <textarea
                    v-model="sidebar.followUp"
                    maxlength="500"
                    :placeholder="t('side.followUp.placeholder')"
                    @keydown.ctrl.enter.prevent="submitFollowUp"
                    @keydown.meta.enter.prevent="submitFollowUp"
                ></textarea>
                <button :disabled="sidebar.followUpLoading || !sidebar.followUp.trim()" @click="submitFollowUp">
                  {{ sidebar.followUpLoading ? t('side.followUp.loading') : t('side.followUp.send') }}
                </button>
              </div>
              <div class="feedback-row">
                <span>{{ t('side.helpful') }}</span>
                <button :disabled="sidebar.feedbackLoading" :class="{ active: sidebar.feedback === 1 }" :aria-pressed="sidebar.feedback === 1" @click="sendFeedback(1)" :title="t('side.up.title')">{{ t('side.up') }}</button>
                <button :disabled="sidebar.feedbackLoading" :class="{ active: sidebar.feedback === -1 }" :aria-pressed="sidebar.feedback === -1" @click="sendFeedback(-1)" :title="t('side.down.title')">{{ t('side.down') }}</button>
              </div>
            </div>
            <div v-else class="text-content">
              <p v-if="sidebar.error" class="inline-error" role="alert">{{ sidebar.error }}</p>
              <template v-if="sidebar.content">
                <div class="result-actions">
                  <button type="button" @click="copyResult">{{ t('side.copyAll') }}</button>
                  <button type="button" @click="downloadResult">{{ t('side.exportText') }}</button>
                </div>
                <p class="text-meta">{{ transcriptMeta }}</p>
                <pre>{{ sidebar.content }}</pre>
              </template>
              <p v-else-if="!sidebar.error" class="text-meta">{{ t('side.noTranscript') }}</p>
            </div>
          </div>
        </div>
      </div>

      <div v-if="showAuthModal" class="auth-backdrop" @click.self="closeAuthModal">
        <div
            ref="authPanel"
            class="auth-panel"
            role="dialog"
            aria-modal="true"
            aria-labelledby="auth-title"
            @keydown="trapAuthFocus"
        >
          <div class="auth-header">
            <h2 id="auth-title" class="auth-title">{{ authMode === 'login' ? t('auth.login') : t('auth.register') }}</h2>
            <button class="close-btn" @click="closeAuthModal" :aria-label="t('auth.close')">×</button>
          </div>
          <form class="auth-body" @submit.prevent="handleAuth">
            <!-- 公开演示服务器：注册需内测码，演示账号每天有 AI 额度 -->
            <div v-if="authConfig.inviteOnly || authConfig.demo" class="beta-note">
              <p class="beta-title">{{ t('auth.beta.title') }}</p>
              <p v-if="authConfig.demo" class="beta-demo">
                {{ t('auth.beta.demo') }} <code>{{ authConfig.demo.username }}</code> / <code>{{ authConfig.demo.password }}</code>
                <span v-if="authConfig.limited"> · {{ t('auth.beta.limit') }}</span>
                <button type="button" class="toggle-link" @click="fillDemo">{{ t('auth.beta.fill') }}</button>
              </p>
              <p v-if="authConfig.inviteOnly" class="beta-invite">{{ t('auth.beta.invite') }}</p>
            </div>
            <div class="input-group">
              <label for="auth-username">{{ t('auth.username') }}</label>
              <input id="auth-username" v-model="authForm.username" type="text" :placeholder="t('auth.username.placeholder')" autocomplete="username" autofocus />
            </div>
            <div class="input-group">
              <label for="auth-password">{{ t('auth.password') }}</label>
              <input id="auth-password" v-model="authForm.password" type="password" :placeholder="t('auth.password.placeholder')" :autocomplete="authMode === 'login' ? 'current-password' : 'new-password'" />
            </div>
            <div class="input-group" v-if="authMode === 'register' && authConfig.inviteOnly">
              <label for="auth-invite">{{ t('auth.invite') }}</label>
              <input id="auth-invite" v-model="authForm.inviteCode" type="text" :placeholder="t('auth.invite.placeholder')" autocomplete="off" />
            </div>
            <div class="input-group" v-if="authMode === 'register'">
              <label for="auth-nickname">{{ t('auth.nickname') }}</label>
              <input id="auth-nickname" v-model="authForm.nickname" type="text" :placeholder="t('auth.nickname.placeholder')" autocomplete="nickname" />
            </div>
            <div class="auth-action">
              <button type="submit" class="primary-btn" :disabled="authLoading">
                <span v-if="!authLoading">{{ authMode === 'login' ? t('auth.login') : t('auth.register') }}</span>
                <span v-else>{{ t('auth.wait') }}</span>
              </button>
            </div>
            <div class="auth-toggle">
              <span class="toggle-text">{{ authMode === 'login' ? t('auth.noAccount') : t('auth.hasAccount') }}</span>
              <button type="button" class="toggle-link" @click="switchAuthMode()">{{ authMode === 'login' ? t('auth.toRegister') : t('auth.toLogin') }}</button>
            </div>
            <p
                v-if="authMessage"
                class="auth-msg"
                :class="{'error': authError}"
                :role="authError ? 'alert' : 'status'"
                aria-live="polite"
            >{{ authMessage }}</p>
          </form>
        </div>
      </div>
    </main>
  </div>
</template>

<script setup>
import { computed, nextTick, ref, watch, onMounted, onUnmounted } from 'vue'
import { apiRequest, clearAuthToken, hasAuthToken, setAuthToken } from './api'
import {
  forgetUploadProgress,
  formatBytes,
  formatDurationText,
  hasUploadProgress,
  uploadVideoInChunks,
  validateVideoFile
} from './chunkUpload'
import { DEMO_ITEM } from './demoData'
import { LOCALES, locale, localeTag, setLocale, t } from './i18n'

import { createTaskStreams } from './taskEvents'
import { useAnalysisWorkspace } from './useAnalysisWorkspace'

// --- 变量定义 ---
const DEMO_MODE = new URLSearchParams(window.location.search).has('demo')
const MESSAGE_TIMEOUT_MS = 4000
const file = ref(null)
const videoUrl = ref('')
const message = ref('')
const messageIsError = ref(false)
const uploading = ref(false)
const uploadProgress = ref({ label: t('upload.prepare'), filename: '', percent: null, detail: '', warning: '' })
const uploadAbort = ref(null)
const resumableFile = ref(null)
const resumableChunks = ref({ done: 0, total: 0 })
const list = ref([])
const searchQuery = ref('')
const videoPlayer = ref(null)
const sidebarPanel = ref(null)
const sidebarBody = ref(null)
const authPanel = ref(null)
const deletingId = ref(null)
const isOffline = ref(typeof navigator !== 'undefined' && navigator.onLine === false)
const activeTasks = ref([])
const elapsedSeconds = ref(0)
const visibleList = computed(() => {
  const query = searchQuery.value.trim().toLocaleLowerCase()
  if (!query) return list.value
  return list.value.filter(item => item.filename?.toLocaleLowerCase().includes(query))
})
const isDragOver = ref(false)
const currentUser = ref(null)
const showAuthModal = ref(false)
const authMode = ref('login')
const authLoading = ref(false)
const authMessage = ref('')
const authError = ref(false)
const authForm = ref({ username: '', password: '', nickname: '', inviteCode: '' })
const authConfig = ref({ inviteOnly: false, demo: null, limited: false })
const loadAuthConfig = async () => {
  try {
    const res = await apiRequest('/user/auth-config')
    if (res.ok) authConfig.value = { ...authConfig.value, ...(await res.json()) }
  } catch { /* older server or offline: the plain form still works */ }
}
const fillDemo = () => {
  if (authMode.value !== 'login') switchAuthMode()
  authForm.value = { ...authForm.value, username: authConfig.value.demo.username, password: authConfig.value.demo.password }
}
const taskStreams = createTaskStreams({
  onActiveChange: tasks => { activeTasks.value = tasks }
})
const VIDEO_EXTENSIONS = new Set(['mp4', 'mov', 'mkv', 'avi', 'webm', 'm4v'])
let dragDepth = 0
let messageTimer = null
let elapsedTimer = null
let lastUploadProgress = {}
let focusBeforeAuth = null
let focusBeforeSidebar = null

// --- 状态展示 ---
const activeTaskOf = mediaId => activeTasks.value.find(task => String(task.id) === String(mediaId))

const systemStatusText = computed(() => {
  if (isOffline.value) return t('status.offline')
  if (uploading.value) {
    return uploadProgress.value.percent !== null
      ? t('status.uploading', { percent: uploadProgress.value.percent })
      : t('status.processing')
  }
  if (activeTasks.value.length) return t('status.tasks', { count: activeTasks.value.length })
  return t('status.ready')
})

const cardStatusClass = item => {
  if (activeTaskOf(item.id)) return 'processing'
  return mediaStatusClass(item.status)
}
const cardStatusLabel = item => {
  const task = activeTaskOf(item.id)
  if (task) return task.type === 'ai' ? t('lib.state.analysing') : t('lib.state.transcribing')
  return mediaStatusLabel(item.status)
}
const cardStatusTitle = item => {
  const task = activeTaskOf(item.id)
  if (!task) return null
  return task.type === 'ai'
    ? t('lib.state.aiRunning')
    : t('lib.state.asrRunning')
}
const actionTitle = (item, label) => item.status === 'COMPLETED'
  ? null
  : t('lib.notReady', { action: label })

const elapsedLabel = computed(() => {
  const total = elapsedSeconds.value
  if (total < 1) return ''
  const minutes = String(Math.floor(total / 60)).padStart(2, '0')
  return `${minutes}:${String(total % 60).padStart(2, '0')}`
})

const loadingHeadline = computed(() => {
  const fallback = sidebar.value.type === 'ai'
    ? t('side.agentWorking')
    : t('side.asrWorking')
  const headline = sidebar.value.statusMessage || fallback
  // 用“已等待”而不是“已运行”：接管历史任务时计时是从打开面板算起的。
  return elapsedLabel.value ? t('side.waited', { headline, time: elapsedLabel.value }) : headline
})

const transcriptMeta = computed(() => {
  const length = sidebar.value.content?.length || 0
  if (!length) return ''
  return t('side.chars', { count: length.toLocaleString(localeTag()) })
})

const resumeHint = computed(() => {
  const target = resumableFile.value
  if (!target) return ''
  const { done, total } = resumableChunks.value
  const progress = total ? t('upload.resume.done', { percent: Math.round((done / total) * 100) }) : t('upload.resume.kept')
  return t('upload.resume.hint', { name: target.name, progress })
})

// --- 核心业务逻辑 ---

const handleDragEnter = () => {
  dragDepth += 1
  isDragOver.value = true
}

// 拖过子元素也会触发 dragleave，用进出计数避免提示文案反复闪烁。
const handleDragLeave = () => {
  dragDepth = Math.max(0, dragDepth - 1)
  if (!dragDepth) isDragOver.value = false
}

const resetDragState = () => {
  dragDepth = 0
  isDragOver.value = false
}

/** 统一入口：登录、格式、体积三道校验全部在进入上传态之前完成。 */
const startUpload = async (selectedFile, extraFileCount = 0) => {
  if (uploading.value) {
    showMsg(t('msg.busy'), true)
    return
  }
  if (!currentUser.value) {
    showMsg(t('msg.loginFirst'), true)
    openAuthModal()
    return
  }
  if (!selectedFile) return
  if (!isSupportedVideo(selectedFile)) {
    showMsg(t('msg.unsupported', { name: selectedFile.name }), true)
    return
  }
  const invalid = validateVideoFile(selectedFile)
  if (invalid) {
    showMsg(`⚠️ ${invalid}`, true)
    return
  }
  if (extraFileCount > 0) {
    showMsg(t('msg.onlyOne', { name: selectedFile.name, count: extraFileCount }))
  }
  file.value = selectedFile
  videoUrl.value = ''
  await uploadFile()
}

const handleFileChange = async (e) => {
  const selected = e.target.files
  await startUpload(selected?.[0], Math.max(0, (selected?.length || 0) - 1))
  e.target.value = ''
}

const handleDrop = async (e) => {
  resetDragState()
  const dropped = e.dataTransfer?.files
  if (!dropped?.length) return
  await startUpload(dropped[0], dropped.length - 1)
}

const buildUploadWarning = progress => {
  if (progress.retryingCount) {
    return t('upload.retrying', { count: progress.retryingCount, attempt: progress.retryAttempt, max: progress.retryMaxAttempts })
  }
  if (progress.resumedChunks) {
    return t('upload.resumed', { count: progress.resumedChunks })
  }
  return ''
}

const applyUploadProgress = progress => {
  lastUploadProgress = progress
  if (progress.phase === 'hashing') {
    uploadProgress.value = {
      label: t('upload.hashing'),
      filename: file.value?.name || uploadProgress.value.filename,
      percent: progress.percent,
      detail: `${formatBytes(progress.uploadedBytes)} / ${formatBytes(progress.totalBytes)}`,
      warning: ''
    }
    return
  }
  const merging = progress.phase === 'merging'
  const detail = [`${formatBytes(progress.uploadedBytes)} / ${formatBytes(progress.totalBytes)}`]
  detail.push(t('upload.chunks', { done: progress.completedChunks, total: progress.totalChunks }))
  if (!merging && progress.bytesPerSecond) {
    detail.push(`${formatBytes(progress.bytesPerSecond)}/s`)
    const eta = formatDurationText(progress.etaSeconds)
    if (eta) detail.push(t('upload.eta', { eta }))
  }
  uploadProgress.value = {
    label: merging ? t('upload.merging') : t('upload.sending'),
    filename: file.value?.name || uploadProgress.value.filename,
    percent: progress.percent,
    detail: detail.join(' · '),
    warning: buildUploadWarning(progress)
  }
}

const rememberResumableUpload = target => {
  if (!target || !hasUploadProgress(target)) {
    resumableFile.value = null
    return
  }
  resumableFile.value = target
  resumableChunks.value = {
    done: lastUploadProgress.completedChunks || 0,
    total: lastUploadProgress.totalChunks || 0
  }
}

const uploadFile = async () => {
  const target = file.value
  if (!target) return
  if (DEMO_MODE) {
    showMsg(t('msg.demoUpload'))
    return
  }

  const controller = new AbortController()
  uploadAbort.value = controller
  uploading.value = true
  resumableFile.value = null
  lastUploadProgress = {}
  const uploadUserId = currentUser.value?.id
  uploadProgress.value = {
    label: hasUploadProgress(target) ? t('upload.checking') : t('upload.preparing'),
    filename: target.name,
    percent: 0,
    detail: `0 B / ${formatBytes(target.size)}`,
    warning: ''
  }

  try {
    const uploadedMedia = await uploadVideoInChunks(target, applyUploadProgress, controller.signal)
    if (currentUser.value?.id !== uploadUserId) return
    resumableFile.value = null
    showMsg(uploadedMedia?.instant ? t('msg.instant', { name: target.name }) : t('msg.uploaded', { name: target.name }))
    await fetchList({ notify: true })
    openAgent(uploadedMedia)
  } catch (error) {
    if (currentUser.value?.id !== uploadUserId) return
    rememberResumableUpload(target)
    if (error?.aborted) {
      showMsg(t('msg.uploadCancelled'))
      return
    }
    console.error(error)
    showMsg(
      resumableFile.value
        ? t('msg.uploadInterrupted', { error: error.message })
        : t('msg.uploadFailed', { error: error.message }),
      true
    )
  } finally {
    uploading.value = false
    uploadAbort.value = null
    file.value = null
  }
}

const cancelUpload = () => {
  if (!uploadAbort.value) return
  uploadProgress.value = { ...uploadProgress.value, label: t('upload.cancelling'), warning: '' }
  uploadAbort.value.abort()
}

const resumeUpload = async () => {
  const target = resumableFile.value
  if (!target || uploading.value) return
  file.value = target
  await uploadFile()
}

const discardResumableUpload = () => {
  forgetUploadProgress(resumableFile.value)
  resumableFile.value = null
  resumableChunks.value = { done: 0, total: 0 }
  showMsg(t('msg.progressCleared'))
}

const handleUrlUpload = async () => {
  const normalizedUrl = videoUrl.value.trim()
  if (!normalizedUrl) return
  if (uploading.value) {
    showMsg(t('msg.busy'), true)
    return
  }
  if (DEMO_MODE) {
    videoUrl.value = ''
    showMsg(t('msg.demoUrl'))
    return
  }

  if (!currentUser.value) {
    showMsg(t('msg.loginFirst'), true)
    openAuthModal()
    return
  }

  let parsedUrl
  try {
    parsedUrl = new URL(normalizedUrl)
  } catch {
    parsedUrl = null
  }
  if (!parsedUrl || !['http:', 'https:'].includes(parsedUrl.protocol)) {
    showMsg(t('msg.badUrl'), true)
    return
  }

  uploading.value = true
  const uploadUserId = currentUser.value?.id
  uploadProgress.value = {
    label: t('upload.url.parsing'),
    filename: parsedUrl.hostname,
    percent: null,
    detail: t('upload.url.fetching'),
    warning: ''
  }
  messageIsError.value = false
  message.value = t('upload.url.started')

  const formData = new FormData()
  formData.append('url', normalizedUrl)

  try {
    const res = await apiRequest('/media/upload-url', {
      method: 'POST',
      body: formData
    })
    if (!res.ok) throw new Error(await res.text())
    const uploadedMedia = await res.json()
    if (currentUser.value?.id !== uploadUserId) return

    showMsg(t('msg.urlImported'))
    videoUrl.value = ''
    await fetchList({ notify: true })
    openAgent(uploadedMedia)
  } catch (error) {
    console.error(error)
    if (currentUser.value?.id !== uploadUserId) return
    let errMsg = error.message
    if (errMsg.includes('Unsupported URL')) errMsg = t('msg.urlUnsupported')
    showMsg(t('msg.urlFailed', { error: errMsg }), true)
  } finally {
    uploading.value = false
  }
}

/** 成功提示自动消失；错误提示保留到用户点掉，避免关键失败原因 4 秒后就没了。 */
const showMsg = (msg, isError = false) => {
  clearTimeout(messageTimer)
  messageTimer = null
  message.value = msg
  messageIsError.value = isError
  if (isError) return
  messageTimer = setTimeout(() => {
    if (message.value !== msg) return
    message.value = ''
    messageIsError.value = false
  }, MESSAGE_TIMEOUT_MS)
}

const dismissMessage = () => {
  if (!messageIsError.value) return
  clearTimeout(messageTimer)
  messageTimer = null
  message.value = ''
  messageIsError.value = false
}

const fetchList = async ({ notify = false } = {}) => {
  if (DEMO_MODE) return list.value
  if (!currentUser.value) {
    list.value = []
    return list.value
  }
  try {
    // 带时间戳绕开浏览器缓存，避免删除/新增之后列表还是旧的。
    const res = await apiRequest(`/media/list?_t=${Date.now()}`)
    if (res.status === 401) return null
    if (!res.ok) throw new Error(t('msg.listError'))
    list.value = await res.json()
  } catch (error) {
    console.error(error)
    if (notify) showMsg(t('msg.listFailed'), true)
    return null
  }
  return list.value
}

const isSupportedVideo = selectedFile => {
  if (selectedFile.type?.startsWith('video/')) return true
  const extension = selectedFile.name?.split('.').pop()?.toLowerCase()
  return VIDEO_EXTENSIONS.has(extension)
}

const mediaStatusClass = status => ['COMPLETED', 'PROCESSING', 'FAILED'].includes(status)
  ? status.toLowerCase()
  : 'unknown'
const mediaStatusLabel = status => ['COMPLETED', 'PROCESSING', 'FAILED'].includes(status)
  ? t(`lib.state.${status}`)
  : t('lib.state.waiting')

const {
  sidebar,
  goalPresets,
  analysisModes,
  traceStages,
  renderedMarkdown,
  transcribe,
  closeSidebar,
  openAgent,
  submitAgent,
  startNewAnalysis,
  showDemoResult,
  startPlanEdit,
  cancelPlanEdit,
  addPlanTask,
  removePlanTask,
  rerunWithPlan,
  submitFollowUp,
  searchEvidence,
  sendFeedback,
  retryPlayback,
  handlePlaybackError,
  resetWorkspace,
  discardMediaWorkspace,
  formatPercent
} = useAnalysisWorkspace({
  demoMode: DEMO_MODE,
  taskStreams,
  showMessage: showMsg,
  refreshMediaList: fetchList,
  findMediaItem: id => list.value.find(item => item.id === id),
  onAnswerAppended: () => scrollToLatestAnswer()
})

/** 追问的答案追加在长文末尾，主动滚过去，否则用户会以为“点了没反应”。 */
const scrollToLatestAnswer = async () => {
  await nextTick()
  const container = sidebarBody.value?.querySelector('.markdown-content')
  if (!container) return
  const headings = container.querySelectorAll('h2, h3')
  const anchor = headings.length ? headings[headings.length - 1] : container.lastElementChild
  anchor?.scrollIntoView({ behavior: 'smooth', block: 'start' })
}

const seekVideo = seconds => {
  if (!Number.isFinite(seconds)) return
  const player = videoPlayer.value
  if (!player) {
    if (sidebar.value.playbackError) {
      showMsg(t('msg.seekFailed'), true)
    } else if (sidebar.value.playbackLoading) {
      showMsg(t('msg.seekLoading'))
    } else {
      showMsg(t('msg.seekNone'), true)
    }
    return
  }
  if (player.readyState === 0) {
    player.addEventListener('loadedmetadata', () => seekVideo(seconds), { once: true })
    return
  }
  const duration = player.duration
  const maxTime = Number.isFinite(duration) ? Math.max(0, duration - 0.1) : Number.MAX_SAFE_INTEGER
  player.currentTime = Math.min(Math.max(0, seconds), maxTime)
  player.play().catch(() => {})
  player.scrollIntoView({ behavior: 'smooth', block: 'nearest' })
}

const seekEvidence = event => {
  const link = event.target.closest('a[href^="#video-t="]')
  if (!link) return
  event.preventDefault()
  seekVideo(Number(link.getAttribute('href').split('=')[1]))
}

const seekToEvidence = timestampMs => seekVideo(Number(timestampMs) / 1000)
const formatEvidenceTime = timestampMs => {
  const seconds = Math.max(0, Math.floor(Number(timestampMs) / 1000))
  const hours = Math.floor(seconds / 3600)
  const minutes = Math.floor((seconds % 3600) / 60)
  const time = `${String(minutes).padStart(2, '0')}:${String(seconds % 60).padStart(2, '0')}`
  return hours ? `${String(hours).padStart(2, '0')}:${time}` : time
}

/** Clipboard API 在非 HTTPS 环境不可用，这里保留一条降级路径，避免“复制失败”变成死路。 */
const copyToClipboard = async text => {
  try {
    await navigator.clipboard.writeText(text)
    return true
  } catch {
    // 继续走下面的降级方案。
  }
  try {
    const scratch = document.createElement('textarea')
    scratch.value = text
    scratch.setAttribute('readonly', '')
    scratch.style.position = 'fixed'
    scratch.style.top = '0'
    scratch.style.opacity = '0'
    document.body.appendChild(scratch)
    scratch.select()
    const copied = document.execCommand('copy')
    document.body.removeChild(scratch)
    return copied
  } catch {
    return false
  }
}

const copyResult = async () => {
  const content = sidebar.value.content
  if (!content) {
    showMsg(t('msg.nothingToCopy'), true)
    return
  }
  if (await copyToClipboard(content)) showMsg(sidebar.value.type === 'ai' ? t('msg.copiedAnalysis') : t('msg.copiedTranscript'))
  else showMsg(t('msg.copyFailed'), true)
}

const resultFileBaseName = () => {
  const title = sidebar.value.title || ''
  const raw = title.split(' · ').slice(1).join(' · ') || title
  const cleaned = raw.replace(/\.[^/.]+$/, '').replace(/[\\/:*?"<>|]/g, '_').trim()
  return cleaned || (sidebar.value.type === 'ai' ? 'analysis' : 'transcript')
}

const downloadResult = () => {
  const content = sidebar.value.content
  if (!content) {
    showMsg(t('msg.nothingToExport'), true)
    return
  }
  const isMarkdown = sidebar.value.type === 'ai'
  const blob = new Blob([content], {
    type: isMarkdown ? 'text/markdown;charset=utf-8' : 'text/plain;charset=utf-8'
  })
  const url = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = url
  link.download = `${resultFileBaseName()}.${isMarkdown ? 'md' : 'txt'}`
  document.body.appendChild(link)
  link.click()
  document.body.removeChild(link)
  // 立刻 revoke 在部分浏览器会导致下载拿不到内容，延后一拍更稳。
  setTimeout(() => URL.revokeObjectURL(url), 0)
  showMsg(t('msg.exported', { file: link.download }))
}

const deleteItem = async (item) => {
  if (DEMO_MODE) {
    list.value = list.value.filter(i => i.id !== item.id)
    discardMediaWorkspace(item.id)
    showMsg(t('msg.demoRemoved'))
    return
  }
  if (deletingId.value) return
  const runningTask = activeTaskOf(item.id)
  const warning = runningTask
    ? t('msg.deleteRunning')
    : ''
  if (!confirm(t('msg.deleteConfirm', { name: item.filename, warning }))) return
  deletingId.value = item.id
  try {
    const res = await apiRequest(`/media/delete?id=${item.id}`, { method: 'DELETE' })
    const text = await res.text()
    if (res.ok) {
      showMsg(t('msg.deleted', { name: item.filename }))
      list.value = list.value.filter(i => i.id !== item.id)
      discardMediaWorkspace(item.id)
    } else {
      showMsg('❌ ' + text, true)
    }
  } catch (e) {
    showMsg(t('msg.deleteFailed'), true)
  } finally {
    deletingId.value = null
  }
}

const formatTime = (timeStr) => {
  if (!timeStr) return '--'
  const date = new Date(timeStr)
  if (Number.isNaN(date.getTime())) return '--'
  return `${date.getMonth() + 1}/${date.getDate()} ${String(date.getHours()).padStart(2, '0')}:${String(date.getMinutes()).padStart(2, '0')}`
}

const downloadAudio = async (item) => {
  if (DEMO_MODE) {
    showMsg(t('msg.demoAudio', { name: item.filename }))
    return
  }
  let fileName = item.filename || 'audio.mp3';
  fileName = fileName.replace(/\.[^/.]+$/, "") + ".mp3";
  try {
    showMsg(t('msg.audioPreparing'))
    const res = await apiRequest(`/analysis/download?id=${item.id}`)
    // 失败时后端返回的是 JSON 信封，api.js 会把 message 解包给 text()，
    // 这里读出来向上抛，避免把“视频不存在 / 无权访问 / 转码失败”统一显示成同一句话。
    if (!res.ok) throw new Error((await res.text()) || t('msg.retryLater'))
    const blob = await res.blob()
    const downloadUrl = window.URL.createObjectURL(blob)
    const link = document.createElement('a')
    link.href = downloadUrl
    link.download = fileName
    document.body.appendChild(link)
    link.click()
    document.body.removeChild(link)
    window.URL.revokeObjectURL(downloadUrl)
    showMsg(t('msg.audioDone'))
  } catch (e) {
    showMsg(t('msg.audioFailed', { error: e?.message || t('msg.retryLater') }), true)
  }
}

const restoreFocus = element => {
  if (element?.isConnected && typeof element.focus === 'function') element.focus()
}

const openAuthModal = () => {
  if (showAuthModal.value) return
  focusBeforeAuth = document.activeElement
  showAuthModal.value = true
  authMessage.value = ''
  authForm.value = { username: '', password: '', nickname: '', inviteCode: '' }
  loadAuthConfig()
}
const closeAuthModal = () => {
  showAuthModal.value = false
  restoreFocus(focusBeforeAuth)
  focusBeforeAuth = null
}
const closeActiveOverlay = () => {
  if (showAuthModal.value) closeAuthModal()
  else if (sidebar.value.visible) closeSidebar()
}
const handleKeydown = event => {
  if (event.key === 'Escape') closeActiveOverlay()
}

/** 弹窗内循环 Tab，键盘用户不会一路跳到被遮住的背景里。 */
const trapAuthFocus = event => {
  if (event.key !== 'Tab' || !authPanel.value) return
  const focusable = [...authPanel.value.querySelectorAll('button, input, [tabindex]:not([tabindex="-1"])')]
    .filter(element => !element.disabled && element.offsetParent !== null)
  if (!focusable.length) return
  const first = focusable[0]
  const last = focusable[focusable.length - 1]
  const active = document.activeElement
  if (event.shiftKey && (active === first || !authPanel.value.contains(active))) {
    event.preventDefault()
    last.focus()
  } else if (!event.shiftKey && active === last) {
    event.preventDefault()
    first.focus()
  }
}

const switchAuthMode = ({ keepMessage = false } = {}) => {
  authMode.value = authMode.value === 'login' ? 'register' : 'login'
  if (!keepMessage) authMessage.value = ''
}
const handleAuth = async () => {
  if (!authForm.value.username || !authForm.value.password) {
    authMessage.value = t('msg.fillCredentials')
    authError.value = true
    return
  }
  authLoading.value = true
  authMessage.value = ''
  const endpoint = authMode.value === 'login' ? '/user/login' : '/user/register'
  try {
    const res = await apiRequest(endpoint, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(authForm.value)
    })
    if (!res.ok) {
      authMessage.value = (await res.text()) || t('msg.httpFailed', { status: res.status })
      authError.value = true
      return
    }
    const data = await res.json().catch(() => null)
    if (!data?.userInfo) {
      authMessage.value = t('msg.badResponse')
      authError.value = true
      return
    }
    if (authMode.value === 'login') {
      currentUser.value = data.userInfo
      localStorage.setItem('user', JSON.stringify(data.userInfo))
      setAuthToken(data.token)
      closeAuthModal()
      showMsg(t('msg.welcome', { name: data.userInfo.nickname }))
      fetchList({ notify: true })
    } else {
      authMessage.value = t('msg.registered')
      authError.value = false
      setTimeout(() => switchAuthMode({ keepMessage: true }), 900)
    }
  } catch (e) {
    console.error(e)
    authMessage.value = e?.message || t('msg.network')
    authError.value = true
  } finally {
    authLoading.value = false
  }
}
/** 退出与登录失效走同一套清理，避免两处漏掉不同的字段。 */
const resetSessionState = () => {
  uploadAbort.value?.abort()
  uploadAbort.value = null
  taskStreams.stopAll()
  resetWorkspace()
  currentUser.value = null
  list.value = []
  searchQuery.value = ''
  videoUrl.value = ''
  file.value = null
  resumableFile.value = null
  resumableChunks.value = { done: 0, total: 0 }
  uploading.value = false
  localStorage.removeItem('user')
}

const logout = () => {
  if (hasAuthToken()) {
    apiRequest('/user/logout', { method: 'POST' }).catch(() => {})
  }
  resetSessionState()
  clearAuthToken()
  showMsg(t('msg.loggedOut'))
}

const handleAuthExpired = () => {
  resetSessionState()
  showMsg(t('msg.sessionExpired'), true)
  openAuthModal()
}

const handleOnline = () => {
  isOffline.value = false
  showMsg(t('msg.online'))
  if (currentUser.value) fetchList()
}

const handleOffline = () => {
  isOffline.value = true
  showMsg(t('msg.offline'), true)
}

// 上传中误关标签页会白丢已传分片，这里让浏览器先问一句。
const handleBeforeUnload = event => {
  if (!uploading.value) return
  event.preventDefault()
  event.returnValue = ''
}

// 打开弹层时挂上标记类，锁背景滚动的规则只在窄屏生效（见样式里的说明）。
const overlayOpen = computed(() => sidebar.value.visible || showAuthModal.value)
watch(overlayOpen, open => {
  document.body.classList.toggle('overlay-open', open)
})

watch(() => sidebar.value.visible, async visible => {
  if (visible) {
    focusBeforeSidebar = document.activeElement
    await nextTick()
    sidebarPanel.value?.focus()
    return
  }
  restoreFocus(focusBeforeSidebar)
  focusBeforeSidebar = null
})

// 长任务给一个时间锚点，用户才不会怀疑是不是卡死了。
// 从任务第一次提交时算起：关掉面板、重新打开或刷新页面，计时都接着走，不会从零开始。
watch(() => [sidebar.value.loading, sidebar.value.startedAt], ([loading, taskStartedAt]) => {
  clearInterval(elapsedTimer)
  elapsedTimer = null
  if (!loading) {
    elapsedSeconds.value = 0
    return
  }
  const startedAt = taskStartedAt || Date.now()
  const tick = () => { elapsedSeconds.value = Math.max(0, Math.floor((Date.now() - startedAt) / 1000)) }
  tick()
  elapsedTimer = setInterval(tick, 1000)
})

onMounted(() => {
  window.addEventListener('auth-expired', handleAuthExpired)
  window.addEventListener('keydown', handleKeydown)
  window.addEventListener('online', handleOnline)
  window.addEventListener('offline', handleOffline)
  window.addEventListener('beforeunload', handleBeforeUnload)
  if (DEMO_MODE) {
    currentUser.value = { id: 1, nickname: 'Agent Demo' }
    list.value = [DEMO_ITEM]
    openAgent(DEMO_ITEM)
    showDemoResult()
    return
  }
  const savedUser = localStorage.getItem('user')
  if (savedUser && hasAuthToken()) {
    try {
      currentUser.value = JSON.parse(savedUser)
    } catch(e) {}
  }
  fetchList({ notify: Boolean(currentUser.value) })
})
onUnmounted(() => {
  window.removeEventListener('auth-expired', handleAuthExpired)
  window.removeEventListener('keydown', handleKeydown)
  window.removeEventListener('online', handleOnline)
  window.removeEventListener('offline', handleOffline)
  window.removeEventListener('beforeunload', handleBeforeUnload)
  clearTimeout(messageTimer)
  clearInterval(elapsedTimer)
  uploadAbort.value?.abort()
  document.body.classList.remove('overlay-open')
  taskStreams.stopAll()
})
</script>

<style>
/*
 * Nordic palette: snow-paper ground, granite ink, fjord blue for action, lichen green for "ready",
 * Falu red only for errors/destructive. Texture comes from a faint paper grain and wool-soft shadows,
 * not from gradients. The one expressive element is the mountain/fjord line in the hero.
 */
:root {
  --snow: #f2f3ef;
  --paper: #fbfbf8;
  --birch: #e8e6df;
  --wool: #d8d9d2;
  --granite: #1f2a30;
  --stone: #5e6a6e;
  --mist: #8d979a;
  --fjord: #2f5566;
  --fjord-deep: #23434f;
  --fjord-tint: #e3ecee;
  --lichen: #6f8466;
  --lichen-tint: #e6ece1;
  --falu: #8e2f23;
  --falu-tint: #f4e4e0;
  --amber: #9a6b1d;
  --amber-tint: #f4ecdb;

  --radius-sheet: 18px;
  --radius-control: 8px;
  --shadow-wool: 0 1px 0 rgba(31, 42, 48, 0.04), 0 10px 30px -18px rgba(31, 42, 48, 0.28);
  --shadow-lift: 0 1px 0 rgba(31, 42, 48, 0.05), 0 24px 60px -28px rgba(31, 42, 48, 0.38);
  --font-latin: 'Jost', 'Noto Sans SC', system-ui, sans-serif;
  --font-body: 'Noto Sans SC', 'Jost', system-ui, sans-serif;
}

* { box-sizing: border-box; margin: 0; padding: 0; }

html, body, #app { min-height: 100vh; background: var(--snow); overflow-x: hidden; }

.app-stage {
  position: relative; isolation: isolate; min-height: 100vh; color: var(--granite);
  font-family: var(--font-body); font-size: 15px; line-height: 1.65;
}
.paper-grain {
  position: fixed; inset: 0; pointer-events: none; z-index: -1; opacity: 0.5; mix-blend-mode: multiply;
  background-image: url("data:image/svg+xml,%3Csvg viewBox='0 0 220 220' xmlns='http://www.w3.org/2000/svg'%3E%3Cfilter id='n'%3E%3CfeTurbulence type='fractalNoise' baseFrequency='0.9' numOctaves='2' stitchTiles='stitch'/%3E%3CfeColorMatrix values='0 0 0 0 0.12 0 0 0 0 0.16 0 0 0 0 0.19 0 0 0 0.09 0'/%3E%3C/filter%3E%3Crect width='100%25' height='100%25' filter='url(%23n)'/%3E%3C/svg%3E");
}

button { cursor: pointer; font: inherit; color: inherit; background: none; border: none; }
button:disabled { cursor: not-allowed; opacity: 0.45; }
:focus-visible { outline: 2px solid var(--fjord); outline-offset: 2px; }
code { font-family: var(--font-latin); background: var(--birch); padding: 1px 6px; border-radius: 4px; font-size: 0.9em; }

/* ---------- Navigation ---------- */
.navbar {
  position: sticky; top: 0; z-index: 50;
  background: rgba(242, 243, 239, 0.86); backdrop-filter: saturate(1.2) blur(10px);
  border-bottom: 1px solid var(--wool);
}
.nav-content { max-width: 1180px; margin: 0 auto; padding: 14px 32px; display: flex; justify-content: space-between; align-items: center; }
.brand { display: flex; align-items: center; gap: 10px; color: var(--granite); }
.brand-mark { color: var(--fjord); }
.brand-name { font-family: var(--font-latin); font-size: 1.3rem; font-weight: 500; letter-spacing: 0.01em; }
.nav-controls { display: flex; align-items: center; gap: 14px; }

.status-pill { display: flex; align-items: center; gap: 8px; font-size: 0.82rem; color: var(--stone); }
.status-dot { width: 7px; height: 7px; border-radius: 50%; background: var(--lichen); }
.status-pill.is-active .status-dot { background: var(--fjord); animation: breathe 1.6s ease-in-out infinite; }

.lang-switch { display: flex; gap: 2px; padding: 2px; border: 1px solid var(--wool); border-radius: 999px; }
.lang-switch button { padding: 3px 9px; border-radius: 999px; font-size: 0.78rem; font-weight: 600; color: var(--stone); transition: background 0.2s, color 0.2s; }
.lang-switch button:hover { color: var(--granite); }
.lang-switch button.active { background: var(--granite); color: var(--paper); }
.auth-btn {
  padding: 8px 16px; border-radius: var(--radius-control); background: var(--granite); color: var(--paper);
  font-size: 0.88rem; font-weight: 500; transition: background 0.2s;
}
.auth-btn:hover { background: var(--fjord-deep); }
.user-profile { display: flex; align-items: center; gap: 10px; font-size: 0.9rem; }
.user-avatar {
  width: 28px; height: 28px; border-radius: 50%; display: grid; place-items: center;
  background: var(--fjord-tint); color: var(--fjord-deep); font-weight: 700; font-size: 0.85rem;
}
.logout-btn { color: var(--mist); display: flex; padding: 6px; border-radius: 6px; transition: color 0.2s, background 0.2s; }
.logout-btn:hover { color: var(--falu); background: var(--falu-tint); }

/* ---------- Hero ---------- */
.main-container { max-width: 1180px; margin: 0 auto; padding: 72px 32px 96px; }
.hero-section { position: relative; margin-bottom: 72px; display: grid; grid-template-columns: minmax(0, 1fr) 300px; column-gap: 64px; align-items: end; }
.hero-section > .horizon, .hero-section > .upload-wrapper, .hero-section > .notification-bar { grid-column: 1 / -1; }
.hero-copy { max-width: 640px; }
.hero-steps { list-style: none; counter-reset: step; display: flex; flex-direction: column; gap: 18px; padding-bottom: 6px; }
.hero-steps li { counter-increment: step; display: grid; grid-template-columns: 30px 1fr; column-gap: 12px; }
.hero-steps li::before {
  content: counter(step); grid-row: span 2; width: 26px; height: 26px; border-radius: 50%; border: 1px solid var(--fjord);
  color: var(--fjord); display: grid; place-items: center; font-family: var(--font-latin); font-size: 0.85rem;
}
.hero-steps strong { font-size: 0.95rem; }
.hero-steps span { font-size: 0.85rem; color: var(--mist); }
.slogan-main {
  font-family: var(--font-latin); font-weight: 500; font-size: clamp(2.1rem, 4.6vw, 3.5rem);
  line-height: 1.18; letter-spacing: -0.01em; color: var(--granite);
}
.slogan-sub { margin-top: 18px; max-width: 34em; color: var(--stone); font-size: 1.02rem; line-height: 1.8; }

.horizon { display: block; width: 100%; height: 96px; margin: 40px 0 -1px; }
.horizon .ridge.far { fill: #d5dcdb; }
.horizon .ridge.near { fill: #b9c6c7; }
.horizon .waterline { stroke: var(--fjord); stroke-width: 1.2; }

.upload-wrapper { position: relative; }
.upload-magnet {
  position: relative; background: var(--paper); border: 1px solid var(--wool); border-top: none;
  border-radius: 0 0 var(--radius-sheet) var(--radius-sheet); box-shadow: var(--shadow-wool);
  transition: border-color 0.2s, box-shadow 0.2s;
}
.upload-magnet.is-dragover { border-color: var(--fjord); box-shadow: 0 0 0 3px var(--fjord-tint), var(--shadow-lift); }
.split-container { display: grid; grid-template-columns: 1fr 1fr; }
.pane { display: flex; flex-direction: column; align-items: flex-start; gap: 8px; padding: 36px 40px 40px; min-height: 220px; }
.pane-local { cursor: pointer; border-right: 1px solid var(--wool); border-radius: 0 0 0 var(--radius-sheet); transition: background 0.2s; }
.pane-local:hover, .upload-magnet.is-dragover .pane-local { background: var(--fjord-tint); }
.pane-icon { color: var(--fjord); margin-bottom: 6px; }
.pane-title { font-size: 1.2rem; font-weight: 700; color: var(--granite); }
.pane-desc { color: var(--stone); font-size: 0.9rem; max-width: 28em; }
.url-input-box { display: flex; gap: 8px; width: 100%; margin-top: auto; padding-top: 14px; }
.url-input-box input {
  flex: 1; min-width: 0; padding: 10px 12px; border-radius: var(--radius-control); border: 1px solid var(--wool);
  background: var(--snow); color: var(--granite); font-family: var(--font-latin); transition: border-color 0.2s, background 0.2s;
}
.url-input-box input:focus { outline: none; border-color: var(--fjord); background: var(--paper); }
.url-go-btn { padding: 0 18px; border-radius: var(--radius-control); background: var(--fjord); color: var(--paper); font-weight: 500; }
.url-go-btn:not(:disabled):hover { background: var(--fjord-deep); }

.magnet-content.busy { display: flex; flex-direction: column; align-items: flex-start; gap: 10px; padding: 40px; min-height: 220px; justify-content: center; }
.busy-text { font-size: 1.15rem; font-weight: 700; }
.busy-file { color: var(--stone); font-size: 0.9rem; max-width: 100%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.upload-progress { width: 100%; height: 6px; border-radius: 3px; background: var(--birch); overflow: hidden; }
.upload-progress span { display: block; height: 100%; background: var(--fjord); border-radius: 3px; transition: width 0.3s ease; }
.upload-progress.indeterminate span { width: 30%; animation: slide 1.4s ease-in-out infinite; }
.busy-stat { color: var(--stone); font-family: var(--font-latin); font-size: 0.88rem; }
.busy-warning { color: var(--amber); background: var(--amber-tint); padding: 6px 10px; border-radius: 6px; font-size: 0.88rem; }
.busy-actions button, .upload-resume button, .library-empty button {
  padding: 6px 14px; border-radius: var(--radius-control); border: 1px solid var(--wool); background: var(--paper); font-size: 0.88rem;
}
.busy-actions button:hover, .upload-resume button:hover, .library-empty button:hover { border-color: var(--granite); }
.upload-resume { display: flex; flex-wrap: wrap; align-items: center; gap: 10px; margin-top: 14px; color: var(--stone); font-size: 0.9rem; }

.notification-bar {
  position: fixed; left: 50%; bottom: 28px; transform: translateX(-50%); z-index: 1200;
  max-width: min(560px, calc(100vw - 32px)); padding: 12px 18px; border-radius: 12px;
  background: var(--granite); color: var(--paper); font-size: 0.92rem; box-shadow: var(--shadow-lift); cursor: default;
}
.notification-bar.error { background: var(--falu); cursor: pointer; }
.toast-pop-enter-active, .toast-pop-leave-active { transition: opacity 0.25s, transform 0.25s; }
.toast-pop-enter-from, .toast-pop-leave-to { opacity: 0; transform: translate(-50%, 10px); }

/* ---------- Library ---------- */
.workspace-section { margin-top: 8px; }
.section-header { display: flex; justify-content: space-between; align-items: flex-end; gap: 16px; padding-bottom: 14px; border-bottom: 1px solid var(--wool); }
.library-title { display: flex; align-items: baseline; gap: 10px; }
.library-title h2 { font-size: 1.35rem; font-weight: 700; }
.count-chip { font-family: var(--font-latin); color: var(--mist); font-size: 1rem; }
.library-search { display: flex; align-items: center; gap: 8px; padding: 7px 12px; border-radius: var(--radius-control); border: 1px solid var(--wool); background: var(--paper); color: var(--mist); width: min(280px, 50vw); }
.library-search:focus-within { border-color: var(--fjord); color: var(--fjord); }
.library-search input { border: none; outline: none; background: none; color: var(--granite); width: 100%; }

.card-grid { list-style: none; }
.project-card {
  display: flex; align-items: center; justify-content: space-between; gap: 20px;
  padding: 16px 8px; border-bottom: 1px solid var(--wool); transition: background 0.2s;
}
.project-card:hover { background: rgba(251, 251, 248, 0.8); }
.card-meta { display: flex; align-items: center; gap: 14px; min-width: 0; }
.meta-icon { flex: none; width: 42px; height: 42px; border-radius: 10px; display: grid; place-items: center; background: var(--birch); color: var(--fjord); }
.meta-info { min-width: 0; }
.filename-mask { font-weight: 500; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; max-width: 46ch; }
.meta-tags { display: flex; align-items: center; gap: 10px; margin-top: 2px; font-size: 0.82rem; color: var(--mist); }
.time-tag { font-family: var(--font-latin); }
.status-indicator { padding: 1px 8px; border-radius: 999px; font-size: 0.78rem; background: var(--birch); color: var(--stone); }
.status-indicator.completed { background: var(--lichen-tint); color: #4c5f45; }
.status-indicator.processing { background: var(--fjord-tint); color: var(--fjord-deep); }
.status-indicator.failed { background: var(--falu-tint); color: var(--falu); }

.action-dock { display: flex; align-items: center; gap: 6px; flex: none; }
.dock-item { padding: 7px 14px; border-radius: var(--radius-control); font-size: 0.88rem; color: var(--stone); transition: background 0.2s, color 0.2s; }
.dock-item:not(:disabled):hover { background: var(--birch); color: var(--granite); }
.dock-item.ai-core { background: var(--fjord); color: var(--paper); font-weight: 500; }
.dock-item.ai-core:not(:disabled):hover { background: var(--fjord-deep); color: var(--paper); }
.delete-btn { margin-left: 4px; padding: 7px; border-radius: var(--radius-control); color: var(--mist); display: flex; transition: color 0.2s, background 0.2s; }
.delete-btn:not(:disabled):hover { color: var(--falu); background: var(--falu-tint); }
.library-empty, .empty-library { padding: 40px 8px; color: var(--stone); display: flex; align-items: center; gap: 14px; }

/* ---------- Side sheet (Agent / transcript) ---------- */
.sidebar-backdrop { position: fixed; inset: 0; background: rgba(31, 42, 48, 0.28); backdrop-filter: blur(2px); z-index: 998; }
.sidebar-panel {
  position: fixed; top: 0; right: 0; width: 880px; max-width: calc(100vw - 24px); height: 100%; z-index: 999;
  display: flex; flex-direction: column; background: var(--paper); border-left: 1px solid var(--wool);
  box-shadow: -30px 0 80px -40px rgba(31, 42, 48, 0.45);
  transform: translateX(105%); transition: transform 0.38s cubic-bezier(0.22, 0.8, 0.26, 1); visibility: hidden;
}
.sidebar-panel.is-open { transform: none; visibility: visible; }
.sidebar-header { display: flex; justify-content: space-between; align-items: center; padding: 18px 28px; border-bottom: 1px solid var(--wool); background: var(--snow); }
.sidebar-title { display: flex; align-items: center; gap: 10px; font-size: 1.12rem; font-weight: 700; min-width: 0; }
.sidebar-title .icon { color: var(--fjord); display: flex; }
.close-btn { width: 34px; height: 34px; border-radius: 50%; font-size: 1.4rem; line-height: 1; color: var(--stone); transition: background 0.2s; }
.close-btn:hover { background: var(--birch); color: var(--granite); }
.sidebar-body { flex: 1; overflow-y: auto; padding: 26px 28px 40px; display: flex; flex-direction: column; gap: 22px; }

.video-evidence video { width: 100%; max-height: 360px; border-radius: 12px; background: var(--granite); display: block; }
.video-evidence p { margin-top: 8px; color: var(--mist); font-size: 0.84rem; }
.video-evidence-loading, .video-evidence-error { padding: 28px; border-radius: 12px; background: var(--birch); color: var(--stone); display: flex; flex-direction: column; gap: 12px; align-items: flex-start; }
.video-evidence-error { background: var(--falu-tint); color: var(--falu); }
.video-evidence-error button { padding: 6px 14px; border-radius: var(--radius-control); background: var(--paper); border: 1px solid currentColor; }

.agent-composer { display: flex; flex-direction: column; gap: 12px; }
.agent-caption { font-weight: 700; font-size: 0.95rem; }
.goal-presets { display: grid; grid-template-columns: repeat(auto-fill, minmax(180px, 1fr)); gap: 8px; }
.goal-presets button {
  text-align: left; padding: 12px 14px; border-radius: 12px; border: 1px solid var(--wool); background: var(--paper);
  display: flex; flex-direction: column; gap: 2px; transition: border-color 0.2s, background 0.2s;
}
.agent-mode-row { grid-template-columns: repeat(5, minmax(0, 1fr)); }
.goal-presets button strong { font-size: 0.92rem; }
.goal-presets button span { font-size: 0.8rem; color: var(--stone); line-height: 1.5; }
.goal-presets button:hover { border-color: var(--mist); }
.goal-presets button.active { border-color: var(--fjord); background: var(--fjord-tint); }
.agent-composer textarea, .follow-up-box textarea {
  width: 100%; min-height: 110px; resize: vertical; padding: 12px 14px; border-radius: 12px; border: 1px solid var(--wool);
  background: var(--snow); color: var(--granite); line-height: 1.7;
}
.agent-composer textarea:focus, .follow-up-box textarea:focus { outline: none; border-color: var(--fjord); background: var(--paper); }
.field-counter { font-size: 0.8rem; color: var(--mist); text-align: right; }
.inline-error { padding: 10px 12px; border-radius: 8px; background: var(--falu-tint); color: var(--falu); font-size: 0.9rem; }
.agent-run-btn, .primary-btn {
  align-self: flex-start; padding: 11px 26px; border-radius: var(--radius-control); background: var(--fjord); color: var(--paper);
  font-weight: 500; transition: background 0.2s;
}
.agent-run-btn:not(:disabled):hover, .primary-btn:not(:disabled):hover { background: var(--fjord-deep); }

.agent-running { display: flex; flex-direction: column; gap: 18px; }
.loading-state { display: flex; flex-direction: column; align-items: flex-start; gap: 8px; padding: 22px; border-radius: 14px; background: var(--snow); border: 1px solid var(--wool); }
.loader { width: 22px; height: 22px; border-radius: 50%; border: 2px solid var(--wool); border-top-color: var(--fjord); animation: spin 0.9s linear infinite; }
.loading-state p { font-weight: 500; }
.loading-state .loading-hint { font-weight: 400; color: var(--mist); font-size: 0.85rem; }
.stream-offline { color: var(--amber) !important; font-size: 0.85rem; font-weight: 400 !important; }
.agent-meta-block { display: flex; flex-direction: column; gap: 8px; }
.meta-label { font-size: 0.82rem; color: var(--mist); }
.agent-meta-block ol { padding-left: 1.3em; display: flex; flex-direction: column; gap: 4px; }
.stage-list { display: flex; flex-wrap: wrap; gap: 6px; }
.stage-list span { padding: 3px 10px; border-radius: 999px; background: var(--birch); font-family: var(--font-latin); font-size: 0.8rem; color: var(--stone); }

.result-actions { display: flex; flex-wrap: wrap; gap: 8px; }
.result-actions button, .plan-edit-trigger, .plan-editor button, .feedback-row button {
  padding: 6px 14px; border-radius: var(--radius-control); border: 1px solid var(--wool); background: var(--paper); font-size: 0.86rem;
  transition: border-color 0.2s, background 0.2s;
}
.result-actions button:not(:disabled):hover, .plan-edit-trigger:hover, .plan-editor button:not(:disabled):hover, .feedback-row button:not(:disabled):hover { border-color: var(--granite); }

.evidence-search { display: flex; flex-direction: column; gap: 10px; }
.evidence-search-form { display: flex; gap: 8px; }
.evidence-search-form input { flex: 1; min-width: 0; padding: 9px 12px; border-radius: var(--radius-control); border: 1px solid var(--wool); background: var(--snow); }
.evidence-search-form input:focus { outline: none; border-color: var(--fjord); background: var(--paper); }
.evidence-search-form button { padding: 0 16px; border-radius: var(--radius-control); background: var(--granite); color: var(--paper); }
.evidence-search-error { color: var(--falu); font-size: 0.86rem; }
.evidence-search-results { display: flex; flex-direction: column; gap: 6px; }
.evidence-search-results button {
  text-align: left; display: grid; grid-template-columns: auto auto 1fr; align-items: baseline; gap: 10px;
  padding: 10px 12px; border-radius: 10px; background: var(--snow); border: 1px solid transparent;
}
.evidence-search-results button:hover { border-color: var(--fjord); }
.evidence-search-results strong { font-family: var(--font-latin); color: var(--fjord); }
.evidence-search-results small { color: var(--mist); font-size: 0.76rem; }
.evidence-search-results span { font-size: 0.88rem; color: var(--stone); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

.markdown-content { font-size: 0.98rem; line-height: 1.85; color: var(--granite); max-width: 70ch; }
.markdown-content h1, .markdown-content h2 { font-size: 1.15rem; margin: 1.6em 0 0.5em; padding-bottom: 6px; border-bottom: 1px solid var(--wool); }
.markdown-content h1:first-child, .markdown-content h2:first-child { margin-top: 0; font-size: 1.4rem; border-bottom: none; }
.markdown-content h3 { font-size: 1rem; margin: 1.2em 0 0.4em; }
.markdown-content p { margin: 0.6em 0; }
.markdown-content ul, .markdown-content ol { padding-left: 1.3em; margin: 0.4em 0; }
.markdown-content li { margin: 0.35em 0; }
.markdown-content li::marker { color: var(--mist); }
.markdown-content blockquote { margin: 0 0 1em; padding: 10px 14px; border-radius: 10px; background: var(--amber-tint); color: #6d4c15; font-size: 0.9rem; }
.markdown-content blockquote p { margin: 0; }
.markdown-content code { background: var(--birch); }
.markdown-content a { color: var(--fjord); }
.markdown-content a[href^="#video-t="] {
  display: inline-block; padding: 0 7px; border-radius: 6px; background: var(--fjord-tint); color: var(--fjord-deep);
  font-family: var(--font-latin); font-weight: 500; text-decoration: none; transition: background 0.2s, color 0.2s;
}
.markdown-content a[href^="#video-t="]:hover { background: var(--fjord); color: var(--paper); }

.agent-inspector { border: 1px solid var(--wool); border-radius: 12px; background: var(--snow); }
.agent-inspector summary { padding: 12px 16px; cursor: pointer; font-weight: 500; color: var(--stone); }
.agent-inspector[open] summary { border-bottom: 1px solid var(--wool); }
.agent-inspector-content { padding: 16px; display: flex; flex-direction: column; gap: 16px; }
.plan-editor { display: flex; flex-direction: column; gap: 8px; }
.plan-editor-row { display: flex; gap: 6px; }
.plan-editor-row input { flex: 1; padding: 8px 10px; border-radius: var(--radius-control); border: 1px solid var(--wool); background: var(--paper); }
.plan-editor-actions { display: flex; gap: 8px; justify-content: flex-end; }
.plan-edit-trigger { align-self: flex-start; }
.quality-row { display: flex; flex-wrap: wrap; gap: 8px; }
.quality-row span { padding: 4px 10px; border-radius: 999px; background: var(--lichen-tint); color: #4c5f45; font-size: 0.82rem; }

.follow-up-box { display: flex; gap: 10px; align-items: flex-end; }
.follow-up-box textarea { min-height: 72px; }
.follow-up-box button { flex: none; padding: 10px 20px; border-radius: var(--radius-control); background: var(--fjord); color: var(--paper); font-weight: 500; }
.follow-up-box button:not(:disabled):hover { background: var(--fjord-deep); }
.feedback-row { display: flex; align-items: center; gap: 8px; color: var(--stone); font-size: 0.88rem; }
.feedback-row button.active { background: var(--fjord-tint); border-color: var(--fjord); color: var(--fjord-deep); }

.text-content { display: flex; flex-direction: column; gap: 12px; }
.text-meta { color: var(--mist); font-size: 0.86rem; }
.text-content pre { white-space: pre-wrap; word-break: break-word; font-family: var(--font-body); line-height: 1.9; padding: 20px; border-radius: 12px; background: var(--snow); border: 1px solid var(--wool); max-width: 72ch; }

/* ---------- Auth dialog ---------- */
.auth-backdrop { position: fixed; inset: 0; z-index: 1100; display: grid; place-items: center; padding: 16px; background: rgba(31, 42, 48, 0.32); backdrop-filter: blur(3px); }
.auth-panel { width: min(400px, 100%); background: var(--paper); border-radius: var(--radius-sheet); box-shadow: var(--shadow-lift); border: 1px solid var(--wool); overflow: hidden; }
.auth-header { display: flex; justify-content: space-between; align-items: center; padding: 22px 26px 6px; }
.auth-title { font-size: 1.3rem; font-weight: 700; }
.auth-body { padding: 12px 26px 26px; display: flex; flex-direction: column; gap: 14px; }
.input-group { display: flex; flex-direction: column; gap: 6px; }
.input-group label { font-size: 0.85rem; color: var(--stone); }
.input-group input { padding: 10px 12px; border-radius: var(--radius-control); border: 1px solid var(--wool); background: var(--snow); color: var(--granite); }
.input-group input:focus { outline: none; border-color: var(--fjord); background: var(--paper); }
.auth-action .primary-btn { width: 100%; margin-top: 4px; }
.auth-toggle { display: flex; justify-content: center; gap: 6px; font-size: 0.88rem; color: var(--stone); }
.toggle-link { color: var(--fjord); font-weight: 500; }
.toggle-link:hover { text-decoration: underline; }
.auth-msg { text-align: center; font-size: 0.88rem; color: #4c5f45; }
.beta-note { padding: 12px 14px; border-radius: 10px; background: rgba(191, 138, 48, 0.1); border: 1px solid rgba(191, 138, 48, 0.3); font-size: 0.86rem; line-height: 1.6; }
.beta-note p { margin: 0; }
.beta-note p + p { margin-top: 6px; }
.beta-title { font-weight: 600; }
.beta-note code { padding: 1px 5px; border-radius: 4px; background: rgba(0, 0, 0, 0.06); font-size: 0.84rem; }
.beta-note .toggle-link { margin-left: 6px; }
.auth-msg.error { color: var(--falu); }

/* ---------- Responsive ---------- */
@media (max-width: 820px) {
  .nav-content { padding: 12px 16px; }
  .status-pill .status-text { display: none; }
  .main-container { padding: 40px 16px 72px; }
  .hero-section { grid-template-columns: 1fr; }
  .hero-steps { margin-top: 28px; }
  .agent-mode-row { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .horizon { height: 64px; margin-top: 28px; }
  .split-container { grid-template-columns: 1fr; }
  .pane { padding: 26px 22px; min-height: 0; }
  .pane-local { border-right: none; border-bottom: 1px solid var(--wool); border-radius: 0; }
  .section-header { flex-direction: column; align-items: stretch; }
  .library-search { width: 100%; }
  .project-card { flex-direction: column; align-items: stretch; gap: 12px; }
  .filename-mask { max-width: 100%; }
  .action-dock { flex-wrap: wrap; }
  .sidebar-panel { max-width: 100vw; width: 100vw; }
  .sidebar-header { padding: 14px 16px; }
  .sidebar-body { padding: 18px 16px 32px; }
  .follow-up-box { flex-direction: column; align-items: stretch; }
  .user-name { display: none; }
  body.overlay-open { overflow: hidden; }
}
body.overlay-open { overflow: hidden; }

@media (prefers-reduced-motion: reduce) {
  *, *::before, *::after { animation-duration: 0.01ms !important; animation-iteration-count: 1 !important; transition-duration: 0.01ms !important; }
}

@keyframes spin { to { transform: rotate(360deg); } }
@keyframes breathe { 50% { opacity: 0.35; } }
@keyframes slide { 0% { transform: translateX(-100%); } 100% { transform: translateX(340%); } }
</style>
