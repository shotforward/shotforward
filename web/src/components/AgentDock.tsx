import {
  ChangeEvent,
  ClipboardEvent,
  FormEvent,
  KeyboardEvent,
  PointerEvent,
  useEffect,
  useMemo,
  useRef,
  useState,
} from 'react';
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link } from 'react-router';
import { useTranslation } from 'react-i18next';
import {
  compactSessionContext,
  getSessionMessages,
  getSessionSnapshot,
  interruptAgentTurn,
  listSessions,
  submitAgentInput,
  uploadAgentMedia,
  undoChangeSet,
  type AgentSession,
  type AgentFocus,
  type AgentConversationTurn,
  type AgentConversationMessage,
  type AgentConversationMedia,
  type AgentMediaReference,
  type AgentVideoPromptTarget,
  type ChangeSetSummary,
  type SessionSnapshot,
} from '../api/agent';
import { agentRuntimeOptions } from '../agentRuntime';
import { businessErrorCode, businessErrorMessage } from '../businessErrors';
import { RechargeAction } from './RechargeAction';
import {
  clearSessionPreparation,
  prepareAgentSession,
  sessionPreparationKey,
  type AgentSessionPreparation,
} from '../agentSessionPreparation';
import { listProviders } from '../api/providers';
import { resolveVideoPromptTarget } from '../videoPromptTarget';
import type { RuntimeModel } from '../api/runtime';
import { APIError, createOperationKey } from '../api/client';
import {
  artifactContentURL,
  assignArtifact,
  createAsset,
  getMediaSlot,
  listEpisodeAssets,
  listEpisodeReferenceMedia,
  type AssetKind,
  type EpisodeAsset,
  type EpisodeReferenceMedia,
} from '../api/assets';
import { saveDownload } from '../desktop';
import { useAgentEvents } from '../api/useAgentEvents';
import {
  agentDockWidth,
  clampAgentDockWidth,
  defaultAgentDockWidth,
  preferredAgentModelForRuntime,
  setAgentDockWidth,
  setPreferredAgentModelForRuntime,
} from '../preferences';
import { AgentMarkdown } from './AgentMarkdown';
import { Select } from './Select';
import { Dialog } from './Dialog';
import { formatAgentElapsed, useAgentTurnElapsed } from './AgentDockProgress';
import {
  agentMediaErrorMessageKey,
  clipboardImageFiles,
  maximumAgentAttachments,
  prepareAgentImageFiles,
} from './AgentDockMedia';
import '../styles/agent-dock.css';

const liveSnapshotPollIntervalMilliseconds = 1_000;

interface OptimisticInput {
  sessionID: string;
  turnID: string;
  clientOperationID: string;
  action: 'queue_turn' | 'steer_turn';
  previousSameTextCounts: Record<string, number>;
  text: string;
  media: AgentConversationMedia[];
}

interface AgentSubmission {
  projectID: string;
  episodeID: string;
  sessionID: string;
  modelID: string;
  text: string;
  focus: AgentFocus;
  attachments: AgentConversationMedia[];
  clientOperationID: string;
  attachmentWorkKey: string;
  videoPromptTarget?: AgentVideoPromptTarget;
  isCurrentUI: () => boolean;
}

interface MediaAction {
  media: AgentConversationMedia;
}

interface AttachmentWork {
  key: string;
  reserved: number;
  queue: Promise<void>;
}

interface ResizeGesture {
  pointerID: number;
  startX: number;
  startWidth: number;
  currentWidth: number;
}

export interface AgentDraftRequest {
  id: number;
  text?: string;
  reference?:
    { kind: 'asset'; value: EpisodeAsset } | { kind: 'uploaded_media'; value: EpisodeReferenceMedia };
}

export type AgentContext = 'script' | 'assets' | 'storyboard';

export function AgentDock({
  projectID,
  episodeID,
  focusSessionID,
  draftRequest,
  onDraftApplied,
  context = 'script',
  contextTitle,
  focus = {},
  videoPromptTarget,
}: {
  projectID: string;
  episodeID: string;
  focusSessionID?: string;
  draftRequest?: AgentDraftRequest;
  onDraftApplied?: (id: number) => void;
  context?: AgentContext;
  contextTitle?: string;
  focus?: AgentFocus;
  videoPromptTarget?: AgentVideoPromptTarget;
}) {
  const { t, i18n } = useTranslation();
  const queryClient = useQueryClient();
  const dockRef = useRef<HTMLElement>(null);
  const historyRef = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLTextAreaElement>(null);
  const fileInputRef = useRef<HTMLInputElement>(null);
  const sessionSwitcherRef = useRef<HTMLDivElement>(null);
  const assetMentionRef = useRef<HTMLDivElement>(null);
  const historyMetrics = useRef<{ height: number; top: number } | undefined>(undefined);
  const resizeGesture = useRef<ResizeGesture | undefined>(undefined);
  const [dockWidth, setDockWidth] = useState(agentDockWidth);
  const [followLatest, setFollowLatest] = useState(true);
  const contextLabel = {
    script: t('agent.contextScript'),
    assets: t('agent.contextAssets'),
    storyboard: t('agent.contextStoryboard'),
  }[context];
  const runtime = useQuery(agentRuntimeOptions(queryClient));
  const activeRuntimeKind = runtime.data?.active_runtime_kind ?? 'user_codex';
  const preparation = useQuery<AgentSessionPreparation | null>({
    queryKey: sessionPreparationKey(projectID, episodeID, activeRuntimeKind),
    queryFn: () => null,
    enabled: false,
  }).data;
  const preparing = preparation?.status === 'preparing';
  const preparationScreen = preparing || preparation?.status === 'failed';
  const scope = `${projectID}/${episodeID}/${activeRuntimeKind}`;
  const currentScope = useRef(scope);
  currentScope.current = scope;
  const providers = useQuery({
    queryKey: ['generation-providers'],
    queryFn: listProviders,
    staleTime: 30_000,
    enabled: !videoPromptTarget,
  });
  const currentVideoPromptTarget =
    videoPromptTarget ?? resolveVideoPromptTarget(providers.data?.providers ?? []);
  const sessions = useQuery({
    queryKey: ['agent-sessions', projectID, episodeID],
    queryFn: () => listSessions(projectID, episodeID),
  });
  const [selectedID, setSelectedID] = useState('');
  const [sessionMenuOpen, setSessionMenuOpen] = useState(false);
  const selectableSessions = (sessions.data?.sessions ?? []).filter(
    (session) =>
      !runtime.data?.active_runtime_kind || session.runtime_kind === runtime.data.active_runtime_kind,
  );
  const selectedSession = preparationScreen
    ? undefined
    : ((preparation?.status === 'ready' ? preparation.session : undefined) ??
      selectableSessions.find((session) => session.id === selectedID) ??
      selectableSessions.find((session) => session.status === 'active') ??
      selectableSessions[0]);
  const sessionID = selectedSession?.id;
  const compactionView = useRef({ scope, sessionID });
  if (compactionView.current.scope !== scope || compactionView.current.sessionID !== sessionID) {
    compactionView.current = { scope, sessionID };
  }
  const compactionRequests = useRef(new Set<string>());
  const [pendingCompactions, setPendingCompactions] = useState(new Set<string>());
  const compactionKey = `${scope}/${sessionID ?? ''}`;
  const snapshot = useQuery({
    queryKey: ['agent-session', sessionID],
    queryFn: () => getSessionSnapshot(sessionID!),
    enabled: Boolean(sessionID),
    refetchInterval: (query) =>
      pendingCompactions.has(compactionKey) ||
      (query.state.data as SessionSnapshot | undefined)?.context_compacting ||
      (query.state.data as SessionSnapshot | undefined)?.turns.some((turn) => isLiveTurnStatus(turn.status))
        ? liveSnapshotPollIntervalMilliseconds
        : false,
  });
  const canSend = (snapshot.data?.session.status ?? selectedSession?.status) === 'active';
  const conversation = useInfiniteQuery({
    queryKey: ['agent-conversation', sessionID],
    queryFn: ({ pageParam }) => getSessionMessages(sessionID!, pageParam),
    initialPageParam: '',
    getNextPageParam: (page) => page.next_cursor || undefined,
    enabled: Boolean(sessionID),
    staleTime: 15_000,
    retry: (failureCount, error) => isConversationHistoryInitializing(error) && failureCount < 4,
    retryDelay: (failureCount) => Math.min(250 * 2 ** failureCount, 2_000),
  });
  const latestSnapshotTurn = [...(snapshot.data?.turns ?? [])].sort(compareTurns).at(-1);
  const conversationTurns = useMemo(
    () =>
      chronologicalTurns(conversation.data?.pages ?? []).map((item) => {
        const authoritative = snapshot.data?.turns.find((turn) => turn.id === item.turn.id);
        return authoritative ? { ...item, turn: authoritative } : item;
      }),
    [conversation.data, snapshot.data?.turns],
  );
  const liveTurn = snapshot.data
    ? [...snapshot.data.turns]
        .sort(compareTurns)
        .reverse()
        .find((turn) => isLiveTurnStatus(turn.status))
    : [...conversationTurns].reverse().find(({ turn }) => isLiveTurnStatus(turn.status))?.turn;
  const stream = useAgentEvents(
    canSend ? sessionID : undefined,
    snapshot.data?.cursor,
    liveTurn ?? (snapshot.data ? null : undefined),
  );
  const elapsedSeconds = useAgentTurnElapsed(sessionID, liveTurn);
  const [input, setInput] = useState('');
  const [clientOperationID, setClientOperationID] = useState(createOperationKey);
  const receivingContext = useRef({ scope, sessionID, clientOperationID });
  receivingContext.current = { scope, sessionID, clientOperationID };

  function captureReceivingContext(targetSessionID = sessionID) {
    const captured = receivingContext.current;
    const key = sessionPreparationKey(projectID, episodeID, activeRuntimeKind);
    const operationID = queryClient.getQueryData<AgentSessionPreparation>(key)?.operationID;
    const snapshotKey = ['agent-session', targetSessionID];
    const owner = queryClient.getQueryCache().find({ queryKey: snapshotKey, exact: true });
    return () => {
      const current = receivingContext.current;
      const state = queryClient.getQueryData<AgentSessionPreparation>(key);
      const recipient = state?.status === 'ready' ? state.session?.id : current.sessionID;
      return (
        current.scope === captured.scope &&
        recipient === targetSessionID &&
        current.clientOperationID === captured.clientOperationID &&
        state?.operationID === operationID &&
        state?.status !== 'preparing' &&
        state?.status !== 'failed' &&
        queryClient.getQueryCache().find({ queryKey: snapshotKey, exact: true }) === owner
      );
    };
  }
  const [attachments, setAttachments] = useState<AgentConversationMedia[]>([]);
  const attachmentsRef = useRef(attachments);
  attachmentsRef.current = attachments;
  const attachmentWorkKey = `${scope}/${sessionID ?? ''}/${clientOperationID}`;
  const attachmentWork = useRef<AttachmentWork>({
    key: attachmentWorkKey,
    reserved: 0,
    queue: Promise.resolve(),
  });
  if (attachmentWork.current.key !== attachmentWorkKey) {
    attachmentWork.current = { key: attachmentWorkKey, reserved: 0, queue: Promise.resolve() };
  }
  const [pendingMedia, setPendingMedia] = useState({ key: attachmentWorkKey, count: 0 });
  const pendingMediaCount = pendingMedia.key === attachmentWorkKey ? pendingMedia.count : 0;
  const submittingAttachmentsFor = useRef<string | undefined>(undefined);
  function setPendingMediaCount(count: number) {
    setPendingMedia({ key: attachmentWork.current.key, count });
  }
  const uploadingMedia = pendingMediaCount > 0;
  const [mediaError, setMediaError] = useState('');
  const [assetPickerOpen, setAssetPickerOpen] = useState(false);
  const [mediaAction, setMediaAction] = useState<MediaAction>();
  function updateAttachments(
    next: AgentConversationMedia[] | ((current: AgentConversationMedia[]) => AgentConversationMedia[]),
  ) {
    const value = typeof next === 'function' ? next(attachmentsRef.current) : next;
    attachmentsRef.current = value;
    setAttachments(value);
  }
  const [optimisticInput, setOptimisticInput] = useState<OptimisticInput>();
  const submittedContexts = useRef(
    new Map<
      string,
      {
        turnID: string;
        focus: AgentFocus;
        videoPromptTarget?: AgentVideoPromptTarget;
      }
    >(),
  );
  const [model, setModel] = useState('');
  const latestTurnModel = latestSnapshotTurn?.model_id;
  const preferredModel = preferredAgentModelForRuntime(activeRuntimeKind);
  const availableModels = (runtime.data?.models ?? []).map(normalizeRuntimeModel);
  const fallback =
    availableModels.find((item) => item.runtime_kind === activeRuntimeKind && item.id === preferredModel) ??
    availableModels[0];
  const fallbackModel = fallback ? `${fallback.runtime_kind}\u001f${fallback.id}` : '';
  const rawSessionModel = selectedSession
    ? `${selectedSession.runtime_kind || 'user_codex'}\u001f${latestTurnModel || selectedSession.model_id}`
    : '';
  const sessionModel = availableModels.some(
    (item) => `${item.runtime_kind}\u001f${item.id}` === rawSessionModel,
  )
    ? rawSessionModel
    : '';
  const requestedModel = availableModels.some((item) => `${item.runtime_kind}\u001f${item.id}` === model)
    ? model
    : '';
  const selectedModel = requestedModel || sessionModel || fallbackModel;
  const [selectedRuntimeKind, selectedModelID] = selectedModel.split('\u001f') as [
    'user_codex' | 'shotforward',
    string,
  ];
  const selectedModelMatchesSession =
    !selectedSession || selectedRuntimeKind === (selectedSession.runtime_kind || 'user_codex');
  const newSessionModel = requestedModel || fallbackModel;
  const [newSessionRuntimeKind, newSessionModelID] = newSessionModel.split('\u001f') as [
    'user_codex' | 'shotforward',
    string,
  ];
  const sessionCreationError = preparation?.status === 'failed' ? preparation.error : undefined;
  const sessionCreationNetworkError =
    sessionCreationError instanceof APIError && sessionCreationError.code === 'runtime_unavailable';
  const sessionCreationRequestID =
    sessionCreationError instanceof APIError ? sessionCreationError.requestID : undefined;
  const runtimeReady = !runtime.isError && runtime.data?.runtime.health === 'ready';
  const runtimeChecking = runtime.isPending || runtime.data?.runtime.health === 'starting';
  const canCreateSession = runtimeReady && Boolean(newSessionModel) && !preparing;
  const failureCopy = i18n.resolvedLanguage?.startsWith('zh')
    ? agentSessionFailureCopy.zh
    : agentSessionFailureCopy.en;
  const conversationMediaCopy = i18n.resolvedLanguage?.startsWith('zh') ? mediaCopy.zh : mediaCopy.en;
  const progressCopy = i18n.resolvedLanguage?.startsWith('zh') ? agentProgressCopy.zh : agentProgressCopy.en;
  const assets = useQuery({
    queryKey: ['assets', projectID, episodeID],
    queryFn: () => listEpisodeAssets(projectID, episodeID),
    enabled: Boolean(sessionID),
  });
  const referenceMedia = useQuery({
    queryKey: ['episode-reference-media', projectID, episodeID],
    queryFn: () => listEpisodeReferenceMedia(projectID, episodeID),
    enabled: Boolean(sessionID && assetPickerOpen),
  });
  const sending = useMutation({
    mutationFn: ({
      sessionID: targetSessionID,
      modelID,
      text,
      focus: submissionFocus,
      attachments: submissionAttachments,
      clientOperationID: operationID,
      videoPromptTarget: submissionVideoPromptTarget,
    }: AgentSubmission) =>
      submitAgentInput(
        targetSessionID,
        text,
        modelID,
        submissionFocus,
        submissionAttachments.map(mediaReference),
        operationID,
        submissionVideoPromptTarget,
      ),
    onMutate: ({
      sessionID: targetSessionID,
      text,
      attachments: submissionAttachments,
      clientOperationID: operationID,
      isCurrentUI,
    }) => {
      if (!isCurrentUI()) return;
      const pending: OptimisticInput = {
        sessionID: targetSessionID,
        turnID: liveTurn?.id ?? '',
        clientOperationID: operationID,
        action: liveTurn ? 'steer_turn' : 'queue_turn',
        previousSameTextCounts: Object.fromEntries(
          conversationTurns.map(({ turn, messages }) => [
            turn.id,
            messages.filter((message) => message.role === 'user' && message.text === text).length,
          ]),
        ),
        text,
        media: submissionAttachments,
      };
      setInput('');
      setOptimisticInput(pending);
      return pending;
    },
    onSuccess: async (
      { action, turn },
      {
        sessionID: targetSessionID,
        projectID: targetProjectID,
        episodeID: targetEpisodeID,
        text,
        focus: submittedFocus,
        attachments: submissionAttachments,
        clientOperationID: operationID,
        videoPromptTarget: submittedTarget,
        isCurrentUI,
      },
      pending,
    ) => {
      submittedContexts.current.set(targetSessionID, {
        turnID: turn.id,
        focus: submittedFocus,
        ...(submittedTarget ? { videoPromptTarget: submittedTarget } : {}),
      });
      if (isCurrentUI()) {
        setOptimisticInput({
          sessionID: targetSessionID,
          turnID: turn.id,
          clientOperationID: operationID,
          action: action === 'steer_turn' ? 'steer_turn' : 'queue_turn',
          previousSameTextCounts: pending?.previousSameTextCounts ?? {},
          text,
          media: submissionAttachments,
        });
        updateAttachments([]);
        setClientOperationID(createOperationKey());
        setMediaError('');
        stream.clearAssistantText();
      }
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['agent-session', targetSessionID] }),
        queryClient.invalidateQueries({ queryKey: ['agent-conversation', targetSessionID] }),
        queryClient.invalidateQueries({ queryKey: ['agent-sessions', targetProjectID, targetEpisodeID] }),
      ]);
    },
    onError: (_error, { text, isCurrentUI }) => {
      if (!isCurrentUI()) return;
      setInput((current) => (!current || current === text ? text : `${text}\n\n${current}`));
      setOptimisticInput(undefined);
    },
    onSettled: (_data, _error, { attachmentWorkKey: submittedKey }) => {
      if (submittingAttachmentsFor.current === submittedKey) submittingAttachmentsFor.current = undefined;
    },
  });
  const sendingCurrent = sending.isPending && sending.variables?.isCurrentUI();
  const undo = useMutation({
    mutationFn: (changeSetID: string) => undoChangeSet(changeSetID),
    onSuccess: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['agent-session', sessionID] }),
        queryClient.invalidateQueries({ queryKey: ['episode', projectID, episodeID] }),
        queryClient.invalidateQueries({ queryKey: ['storyboard', projectID, episodeID] }),
        queryClient.invalidateQueries({ queryKey: ['assets', projectID, episodeID] }),
      ]);
    },
  });
  const hasKnownConversation = Boolean(snapshot.data?.turns.length || conversationTurns.length);
  const conversationHistoryInitializing = isConversationHistoryInitializing(conversation.error);
  const optimistic = optimisticInput;
  const optimisticVisible = optimistic
    ? optimistic.sessionID === sessionID && !hasCanonicalInput(conversationTurns, optimistic)
    : false;
  const optimisticConversationTurn = optimistic?.turnID
    ? conversationTurns.find(({ turn }) => turn.id === optimistic.turnID)?.turn
    : undefined;
  const optimisticSnapshotTurn = optimistic?.turnID
    ? snapshot.data?.turns.find((turn) => turn.id === optimistic.turnID)
    : undefined;
  const optimisticTurn = optimisticSnapshotTurn ?? optimisticConversationTurn;
  const liveReplyIsCanonical = conversationTurns.some(
    ({ turn, messages }) =>
      turn.id === stream.assistantTurnID && messages.some((message) => message.role === 'assistant'),
  );
  const optimisticFollowsStream = Boolean(
    optimisticVisible &&
    optimistic?.action === 'steer_turn' &&
    stream.assistantText &&
    !liveReplyIsCanonical &&
    stream.assistantTurnID === optimistic.turnID,
  );
  const optimisticAttachedToTurn = Boolean(
    optimisticVisible && optimisticConversationTurn && !optimisticFollowsStream,
  );
  const optimisticTerminalTurn =
    optimisticVisible && optimisticTurn && !isLiveTurnStatus(optimisticTurn.status)
      ? optimisticTurn
      : undefined;
  const renderedConversationTurns = useMemo(
    () =>
      attachOptimisticInput(
        conversationTurns,
        optimisticVisible && !optimisticFollowsStream ? optimistic : undefined,
      ),
    [conversationTurns, optimistic, optimisticVisible, optimisticFollowsStream],
  );
  const liveTurnIsCanonical = conversationTurns.some(({ turn }) => turn.id === stream.assistantTurnID);
  const optimisticLiveStatus = optimisticVisible
    ? optimisticTurn && isLiveTurnStatus(optimisticTurn.status)
      ? optimisticTurn.status
      : optimisticTerminalTurn
        ? ''
        : optimistic?.turnID
          ? 'queued'
          : 'connecting'
    : '';
  const liveStatus = liveTurn?.status ?? optimisticLiveStatus;
  const liveProgress = [
    stream.retryingTurnID === liveTurn?.id && stream.retryingTurnID
      ? progressCopy.retrying
      : stream.activity
        ? t('agent.usingTool', { tool: stream.activity })
        : liveStatus === 'running'
          ? progressCopy.waitingForModel
          : '',
    elapsedSeconds !== undefined ? `${progressCopy.elapsed} ${formatAgentElapsed(elapsedSeconds)}` : '',
  ]
    .filter(Boolean)
    .join(' · ');
  const compacting = useMutation({
    mutationFn: ({
      sessionID: targetSessionID,
    }: {
      sessionID: string;
      key: string;
      isCurrentUI: () => boolean;
    }) => compactSessionContext(targetSessionID),
    retry: false,
    onSettled: async (_result, _error, { sessionID: targetSessionID, key }) => {
      try {
        await Promise.all([
          queryClient.invalidateQueries({ queryKey: ['agent-session', targetSessionID], refetchType: 'all' }),
          queryClient.invalidateQueries({ queryKey: ['agent-conversation', targetSessionID] }),
        ]);
      } finally {
        compactionRequests.current.delete(key);
        setPendingCompactions(new Set(compactionRequests.current));
      }
    },
  });
  const contextCompacting =
    pendingCompactions.has(compactionKey) || snapshot.data?.context_compacting === true;
  const compactionResultCurrent = compacting.variables?.isCurrentUI() ?? false;
  const canCompactContext = Boolean(
    sessionID &&
    canSend &&
    runtimeReady &&
    snapshot.data &&
    !liveStatus &&
    !sendingCurrent &&
    !preparing &&
    !uploadingMedia &&
    !contextCompacting,
  );
  function organizeContext() {
    if (
      !sessionID ||
      !canCompactContext ||
      compactionRequests.current.has(compactionKey) ||
      submittingAttachmentsFor.current === attachmentWork.current.key ||
      attachmentWork.current.reserved > 0
    )
      return;
    const view = compactionView.current;
    compactionRequests.current.add(compactionKey);
    setPendingCompactions(new Set(compactionRequests.current));
    setSessionMenuOpen(false);
    compacting.mutate({
      sessionID,
      key: compactionKey,
      isCurrentUI: () => compactionView.current === view,
    });
  }
  const previousCompaction = useRef({ sessionID, compacting: false });
  useEffect(() => {
    if (
      previousCompaction.current.sessionID === sessionID &&
      previousCompaction.current.compacting &&
      snapshot.data &&
      !snapshot.data.context_compacting
    ) {
      void queryClient.invalidateQueries({ queryKey: ['agent-conversation', sessionID] });
    }
    previousCompaction.current = { sessionID, compacting: snapshot.data?.context_compacting === true };
  }, [queryClient, sessionID, snapshot.data?.context_compacting]);
  const interrupting = useMutation({
    mutationFn: ({ sessionID: targetSessionID, turnID }: { sessionID: string; turnID: string }) =>
      interruptAgentTurn(targetSessionID, turnID),
    onSuccess: async (_result, { sessionID: targetSessionID }) => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['agent-session', targetSessionID] }),
        queryClient.invalidateQueries({ queryKey: ['agent-conversation', targetSessionID] }),
      ]);
    },
  });
  const changeSetsByTurn = useMemo(() => {
    const result = new Map<string, ChangeSetSummary[]>();
    for (const item of snapshot.data?.change_sets ?? []) {
      const values = result.get(item.agent_turn_id) ?? [];
      values.push(item);
      result.set(item.agent_turn_id, values);
    }
    return result;
  }, [snapshot.data]);

  useEffect(() => {
    if (preparation?.status !== 'ready' || !preparation.session) return;
    setSelectedID(preparation.session.id);
    setSessionMenuOpen(false);
    setOptimisticInput(undefined);
    clearSessionPreparation(queryClient, projectID, episodeID, activeRuntimeKind);
  }, [preparation, queryClient, projectID, episodeID, activeRuntimeKind]);

  const previousEpisode = useRef(`${projectID}/${episodeID}`);
  useEffect(() => {
    const episode = `${projectID}/${episodeID}`;
    if (previousEpisode.current !== episode) {
      setInput('');
      setSelectedID('');
      setOptimisticInput(undefined);
      previousEpisode.current = episode;
    }
  }, [projectID, episodeID]);

  useEffect(() => {
    setFollowLatest(true);
    setModel('');
    updateAttachments([]);
    setPendingMediaCount(0);
    setClientOperationID(createOperationKey());
    setMediaError('');
    setAssetPickerOpen(false);
  }, [sessionID]);

  useEffect(() => {
    const textarea = inputRef.current;
    if (!sessionID || !textarea) return;
    const maximumHeight = 144;
    const minimumHeight = 42;
    textarea.style.height = '0px';
    const nextHeight = Math.min(maximumHeight, Math.max(minimumHeight, textarea.scrollHeight));
    textarea.style.height = `${nextHeight}px`;
    textarea.style.overflowY = textarea.scrollHeight > maximumHeight ? 'auto' : 'hidden';
  }, [input, sessionID]);

  useEffect(() => {
    if (!snapshot.data?.session) return;
    if (
      snapshot.data.session.status === selectedSession?.status &&
      snapshot.data.session.title === selectedSession?.title
    )
      return;
    void queryClient.invalidateQueries({ queryKey: ['agent-sessions', projectID, episodeID] });
  }, [
    episodeID,
    projectID,
    queryClient,
    selectedSession?.status,
    selectedSession?.title,
    snapshot.data?.session,
  ]);

  useEffect(() => {
    if (!sessionID || !latestSnapshotTurn || isLiveTurnStatus(latestSnapshotTurn.status)) return;
    void queryClient.invalidateQueries({ queryKey: ['agent-conversation', sessionID] });
  }, [latestSnapshotTurn?.id, latestSnapshotTurn?.status, queryClient, sessionID]);

  useEffect(() => {
    if (!focusSessionID) return;
    setSelectedID(focusSessionID);
  }, [focusSessionID]);

  useEffect(() => {
    if (!sessionMenuOpen) return;

    const closeFromOutside = (event: globalThis.PointerEvent) => {
      const target = event.target;
      if (!(target instanceof Node) || !sessionSwitcherRef.current?.contains(target)) {
        setSessionMenuOpen(false);
      }
    };
    const closeFromEscape = (event: globalThis.KeyboardEvent) => {
      if (event.key === 'Escape') setSessionMenuOpen(false);
    };

    document.addEventListener('pointerdown', closeFromOutside, true);
    document.addEventListener('keydown', closeFromEscape);
    return () => {
      document.removeEventListener('pointerdown', closeFromOutside, true);
      document.removeEventListener('keydown', closeFromEscape);
    };
  }, [sessionMenuOpen]);

  useEffect(() => {
    if (!assetPickerOpen) return;

    const dismissFromOutside = (event: globalThis.PointerEvent) => {
      const target = event.target;
      if (!(target instanceof Node) || !assetMentionRef.current?.contains(target)) {
        setAssetPickerOpen(false);
      }
    };
    const dismissFromEscape = (event: globalThis.KeyboardEvent) => {
      if (event.key === 'Escape') setAssetPickerOpen(false);
    };

    document.addEventListener('pointerdown', dismissFromOutside, true);
    document.addEventListener('keydown', dismissFromEscape);
    return () => {
      document.removeEventListener('pointerdown', dismissFromOutside, true);
      document.removeEventListener('keydown', dismissFromEscape);
    };
  }, [assetPickerOpen]);

  useEffect(() => {
    if (!draftRequest) return;
    if (draftRequest.text !== undefined) setInput(draftRequest.text);
    if (draftRequest.reference && !sessionID) return;
    if (draftRequest.reference?.kind === 'asset') void attachAsset(draftRequest.reference.value);
    if (draftRequest.reference?.kind === 'uploaded_media') attachReferenceMedia(draftRequest.reference.value);
    const frame = requestAnimationFrame(() => {
      inputRef.current?.focus();
      onDraftApplied?.(draftRequest.id);
    });
    return () => cancelAnimationFrame(frame);
  }, [draftRequest, onDraftApplied, sessionID]);

  useEffect(() => {
    const element = historyRef.current;
    if (!element || !followLatest) return;
    const frame = requestAnimationFrame(() => {
      element.scrollTop = element.scrollHeight;
    });
    return () => cancelAnimationFrame(frame);
  }, [
    followLatest,
    renderedConversationTurns,
    optimisticVisible,
    stream.assistantText,
    stream.activity,
    snapshot.data?.change_sets,
  ]);

  async function loadEarlier() {
    const element = historyRef.current;
    if (element) historyMetrics.current = { height: element.scrollHeight, top: element.scrollTop };
    await conversation.fetchNextPage();
    requestAnimationFrame(() => {
      const current = historyRef.current;
      const previous = historyMetrics.current;
      if (current && previous) current.scrollTop = previous.top + current.scrollHeight - previous.height;
      historyMetrics.current = undefined;
    });
  }

  function observeHistoryScroll() {
    const element = historyRef.current;
    if (!element) return;
    setFollowLatest(element.scrollHeight - element.scrollTop - element.clientHeight < 48);
  }

  function scrollToLatest() {
    setFollowLatest(true);
    historyRef.current?.scrollTo({ top: historyRef.current.scrollHeight, behavior: 'smooth' });
  }

  function attachFiles(event: ChangeEvent<HTMLInputElement>) {
    const files = [...(event.target.files ?? [])];
    event.target.value = '';
    attachImageFiles(files);
  }

  function pasteImages(event: ClipboardEvent<HTMLTextAreaElement>) {
    // The textarea keeps its native text paste behavior, including mixed
    // image/plain-text payloads. Only actual clipboard Files become attachments.
    attachImageFiles(clipboardImageFiles(event.clipboardData));
  }

  function mediaFailureMessage(error: unknown) {
    const failure = error instanceof APIError ? error : undefined;
    const message = conversationMediaCopy[agentMediaErrorMessageKey(failure?.code, failure?.status)];
    return failure?.requestID
      ? `${message} ${conversationMediaCopy.requestID} ${failure.requestID}`
      : message;
  }

  function reserveAttachmentSlots(count: number): AttachmentWork | undefined {
    const work = attachmentWork.current;
    if (attachmentsRef.current.length + work.reserved + count > maximumAgentAttachments) {
      setMediaError(conversationMediaCopy.imageLimit);
      return undefined;
    }
    work.reserved += count;
    setPendingMediaCount(work.reserved);
    return work;
  }

  function attachmentSubmissionBlocked() {
    if (sendingCurrent || submittingAttachmentsFor.current === attachmentWork.current.key) {
      setMediaError(conversationMediaCopy.whileSubmitting);
      return true;
    }
    return false;
  }

  function releaseAttachmentSlots(work: AttachmentWork, count: number) {
    work.reserved -= count;
    if (attachmentWork.current === work) setPendingMediaCount(work.reserved);
  }

  function attachImageFiles(files: File[]) {
    if (!sessionID || !files.length) return;
    if (attachmentSubmissionBlocked()) return;
    if (!selectedModelMatchesSession) {
      setMediaError(conversationMediaCopy.switchSessionFirst);
      return;
    }
    const prepared = prepareAgentImageFiles(files);
    if (prepared.errorCode) {
      setMediaError(conversationMediaCopy[agentMediaErrorMessageKey(prepared.errorCode)]);
      return;
    }
    const receiving = captureReceivingContext();
    if (!receiving()) return;
    const work = reserveAttachmentSlots(prepared.files.length);
    if (!work) return;
    const isCurrent = () => attachmentWork.current === work && receiving();
    setMediaError('');
    const upload = async () => {
      let remaining = prepared.files.length;
      try {
        for (const file of prepared.files) {
          if (!isCurrent()) return;
          const uploaded = (await uploadAgentMedia(sessionID, clientOperationID, file)).media;
          if (!isCurrent()) return;
          updateAttachments((current) => uniqueMedia([...current, uploaded]));
          remaining -= 1;
          releaseAttachmentSlots(work, 1);
        }
      } catch (error) {
        if (isCurrent()) setMediaError(mediaFailureMessage(error));
      } finally {
        releaseAttachmentSlots(work, remaining);
      }
    };
    // A synchronous reservation makes successive paste events safe before the
    // next React render; the queue serializes file uploads within this draft.
    work.queue = work.queue.then(upload, upload);
  }

  async function attachAsset(asset: EpisodeAsset) {
    if (!sessionID) return;
    if (attachmentSubmissionBlocked()) return;
    if (!selectedModelMatchesSession) {
      setMediaError(conversationMediaCopy.switchSessionFirst);
      return;
    }
    const receiving = captureReceivingContext();
    if (!receiving()) return;
    const work = reserveAttachmentSlots(1);
    if (!work) return;
    const isCurrent = () => attachmentWork.current === work && receiving();
    setMediaError('');
    try {
      const details = await getMediaSlot(projectID, asset.primary_image_slot_id, false);
      if (!isCurrent()) return;
      const version = details.versions.find(
        (candidate) => candidate.media_version_id === details.slot.current_media_version_id,
      );
      const artifact = details.artifacts.find((candidate) => candidate.artifact_id === version?.artifact_id);
      if (!artifact) {
        setMediaError(conversationMediaCopy.assetHasNoImage);
        return;
      }
      updateAttachments((current) =>
        uniqueMedia([
          ...current,
          {
            artifact_id: artifact.artifact_id,
            name: asset.identity.name,
            origin: 'asset',
            mime_type: artifact.mime_type,
            original_filename: artifact.original_filename,
            byte_size: artifact.byte_size,
            width: artifact.width,
            height: artifact.height,
            created_at: artifact.created_at,
          },
        ]),
      );
      appendMention(asset.identity.name);
      setAssetPickerOpen(false);
      requestAnimationFrame(() => inputRef.current?.focus());
    } catch (error) {
      if (isCurrent()) setMediaError(mediaFailureMessage(error));
    } finally {
      releaseAttachmentSlots(work, 1);
    }
  }

  function attachReferenceMedia(value: EpisodeReferenceMedia) {
    if (!sessionID || !captureReceivingContext()()) return;
    if (attachmentSubmissionBlocked()) return;
    if (
      value.artifact.kind === 'image' &&
      !attachmentsRef.current.some((media) => media.artifact_id === value.artifact.artifact_id) &&
      attachmentsRef.current.length + attachmentWork.current.reserved >= maximumAgentAttachments
    ) {
      setMediaError(conversationMediaCopy.imageLimit);
      return;
    }
    setMediaError('');
    if (value.artifact.kind === 'image')
      updateAttachments((current) => uniqueMedia([...current, conversationMediaFromReference(value)]));
    appendMention(value.name);
    setAssetPickerOpen(false);
    requestAnimationFrame(() => inputRef.current?.focus());
  }

  function appendMention(name: string) {
    setInput((current) => {
      const mention = `@${name}`;
      if (current.includes(mention)) return current;
      if (/(^|\s)@$/.test(current)) return `${current.slice(0, -1)}${mention} `;
      return `${current}${current && !current.endsWith(' ') ? ' ' : ''}${mention} `;
    });
  }

  function continueWithMedia(media: AgentConversationMedia) {
    if (attachmentSubmissionBlocked()) return;
    if (
      !attachmentsRef.current.some((value) => value.artifact_id === media.artifact_id) &&
      attachmentsRef.current.length + attachmentWork.current.reserved >= maximumAgentAttachments
    ) {
      setMediaError(conversationMediaCopy.imageLimit);
      return;
    }
    updateAttachments((current) => uniqueMedia([...current, media]));
    setMediaError('');
    requestAnimationFrame(() => inputRef.current?.focus());
  }

  useEffect(() => {
    const layout = dockRef.current?.closest<HTMLElement>(
      '.creative-layout, .assets-agent-layout, .storyboard-layout',
    );
    if (!layout) return;
    layout.style.setProperty('--agent-dock-width', `${dockWidth}px`);
    return () => {
      layout.style.removeProperty('--agent-dock-width');
    };
  }, [dockWidth]);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const text = input.trim();
    const latestPreparation = queryClient.getQueryData<AgentSessionPreparation>(
      sessionPreparationKey(projectID, episodeID, activeRuntimeKind),
    );
    if (
      !text ||
      !sessionID ||
      !canSend ||
      !selectedModel ||
      (!selectedModelMatchesSession && !runtimeReady) ||
      sendingCurrent ||
      submittingAttachmentsFor.current === attachmentWork.current.key ||
      uploadingMedia ||
      attachmentWork.current.reserved > 0 ||
      contextCompacting ||
      compactionRequests.current.has(compactionKey) ||
      preparing ||
      latestPreparation?.status === 'preparing' ||
      latestPreparation?.status === 'failed' ||
      (latestPreparation?.status === 'ready' && latestPreparation.session?.id !== sessionID) ||
      queryClient.getQueryData<AgentSessionPreparation>(
        sessionPreparationKey(projectID, episodeID, selectedRuntimeKind),
      )?.status === 'preparing'
    )
      return;

    if (attachments.length > 0 && !selectedModelMatchesSession) {
      setMediaError(conversationMediaCopy.switchSessionFirst);
      return;
    }

    let targetSessionID = sessionID;
    if (!selectedModelMatchesSession) {
      try {
        const requestedScope = scope;
        setPreferredAgentModelForRuntime(selectedRuntimeKind, selectedModelID);
        const created = await prepareAgentSession(
          queryClient,
          projectID,
          episodeID,
          selectedModelID,
          selectedRuntimeKind,
        );
        if (
          currentScope.current !== requestedScope ||
          !queryClient
            .getQueryData<{ sessions: AgentSession[] }>(['agent-sessions', projectID, episodeID])
            ?.sessions.some((item) => item.id === created.id)
        )
          return;
        setSelectedID(created.id);
        targetSessionID = created.id;
      } catch {
        return;
      }
    }
    // A steer keeps the running Turn's model instructions and MCP context.
    // After a reload that snapshot is unknown, so leave it with the server.
    const savedContext = submittedContexts.current.get(targetSessionID);
    const activeTurnID = liveTurn?.id ?? optimisticInput?.turnID;
    const steering =
      targetSessionID === sessionID && (liveStatus === 'running' || liveStatus === 'waiting_user');
    const submittedContext = steering && savedContext?.turnID === activeTurnID ? savedContext : undefined;
    const submittedVideoPromptTarget = steering
      ? submittedContext?.videoPromptTarget
      : currentVideoPromptTarget;
    // Lock synchronously: a paste in the same event tick must not create
    // another attachment for an operation that is already being submitted.
    submittingAttachmentsFor.current = attachmentWork.current.key;
    sending.mutate({
      projectID,
      episodeID,
      sessionID: targetSessionID,
      modelID: selectedModelID,
      text,
      focus: submittedContext?.focus ?? focus,
      attachments,
      clientOperationID,
      attachmentWorkKey: attachmentWork.current.key,
      isCurrentUI: captureReceivingContext(targetSessionID),
      ...(submittedVideoPromptTarget ? { videoPromptTarget: submittedVideoPromptTarget } : {}),
    });
  }

  function beginResize(event: PointerEvent<HTMLDivElement>) {
    if (window.innerWidth <= 1040 || !dockRef.current) return;
    event.preventDefault();
    const startWidth = dockRef.current.getBoundingClientRect().width || dockWidth;
    resizeGesture.current = {
      pointerID: event.pointerId,
      startX: event.clientX,
      startWidth,
      currentWidth: startWidth,
    };
    event.currentTarget.setPointerCapture(event.pointerId);
    document.body.classList.add('is-resizing-agent-dock');
  }

  function resize(event: PointerEvent<HTMLDivElement>) {
    const gesture = resizeGesture.current;
    if (!gesture || gesture.pointerID !== event.pointerId) return;
    const next = clampAgentDockWidth(gesture.startWidth + gesture.startX - event.clientX);
    gesture.currentWidth = next;
    setDockWidth(next);
  }

  function finishResize(event: PointerEvent<HTMLDivElement>) {
    const gesture = resizeGesture.current;
    if (!gesture || gesture.pointerID !== event.pointerId) return;
    resizeGesture.current = undefined;
    if (event.currentTarget.hasPointerCapture(event.pointerId)) {
      event.currentTarget.releasePointerCapture(event.pointerId);
    }
    document.body.classList.remove('is-resizing-agent-dock');
    setDockWidth(setAgentDockWidth(gesture.currentWidth));
  }

  function resetWidth() {
    const next = setAgentDockWidth(defaultAgentDockWidth);
    setDockWidth(next);
  }

  function resizeWithKeyboard(event: KeyboardEvent<HTMLDivElement>) {
    if (event.key !== 'ArrowLeft' && event.key !== 'ArrowRight' && event.key !== 'Home') return;
    event.preventDefault();
    if (event.key === 'Home') {
      resetWidth();
      return;
    }
    const step = event.shiftKey ? 40 : 16;
    const next = setAgentDockWidth(dockWidth + (event.key === 'ArrowLeft' ? step : -step));
    setDockWidth(next);
  }

  function createNewSession() {
    if (!canCreateSession) return;
    setSessionMenuOpen(false);
    setPreferredAgentModelForRuntime(newSessionRuntimeKind, newSessionModelID);
    void prepareAgentSession(
      queryClient,
      projectID,
      episodeID,
      newSessionModelID,
      newSessionRuntimeKind,
    ).catch(() => {});
  }

  function newSessionControls(placement: 'header' | 'menu' | 'empty' = 'menu') {
    return (
      <button
        type="button"
        className={`${placement === 'empty' ? 'button button--primary ' : ''}agent-new-session-button agent-new-session-button--${placement}`}
        disabled={!canCreateSession}
        onClick={createNewSession}
      >
        {placement !== 'empty' && <span aria-hidden="true">＋</span>}
        {preparation?.status === 'failed' ? t('common.retry') : t('agent.newSession')}
      </button>
    );
  }

  return (
    <aside
      ref={dockRef}
      className="agent-dock"
      style={{ '--agent-dock-current-width': `${dockWidth}px` } as React.CSSProperties}
    >
      <div
        className="agent-resize-handle"
        role="separator"
        aria-label={t('agent.resize')}
        aria-orientation="vertical"
        aria-valuemin={300}
        aria-valuemax={640}
        aria-valuenow={dockWidth}
        tabIndex={0}
        onPointerDown={beginResize}
        onPointerMove={resize}
        onPointerUp={finishResize}
        onPointerCancel={finishResize}
        onDoubleClick={resetWidth}
        onKeyDown={resizeWithKeyboard}
      />
      <header className="agent-dock__header">
        <div className="agent-dock__identity">
          <strong>Agent</strong>
          <span className={stream.connected ? 'is-online' : ''} />
          {runtime.data?.active_runtime_kind && (
            <Link className="agent-runtime-badge" to="/settings/agent">
              {runtime.data.active_runtime_kind === 'shotforward' ? 'ShotForward' : 'Local Codex'}
            </Link>
          )}
          {availableModels.length > 0 && (
            <Select
              className="agent-header-model-select"
              ariaLabel={
                i18n.resolvedLanguage?.startsWith('zh') ? '后续对话使用的模型' : 'Model for following turns'
              }
              value={selectedModel}
              options={availableModels.map((item) => ({
                value: `${item.runtime_kind}\u001f${item.id}`,
                label: item.display_name,
              }))}
              placeholder={t('agent.model')}
              compact
              disabled={Boolean(liveStatus) || sendingCurrent || preparing || contextCompacting}
              onChange={(value) => {
                setModel(value);
                const [runtimeKind, modelID] = value.split('\u001f') as [
                  'user_codex' | 'shotforward',
                  string,
                ];
                setPreferredAgentModelForRuntime(runtimeKind, modelID ?? value);
              }}
            />
          )}
        </div>
        {selectableSessions.length ? (
          <div className="agent-session-actions">
            <div className="agent-session-switcher" ref={sessionSwitcherRef}>
              <button
                type="button"
                aria-label={t('agent.sessionMenu')}
                aria-expanded={sessionMenuOpen}
                disabled={preparing}
                onClick={() => setSessionMenuOpen((open) => !open)}
              >
                <span>{snapshot.data?.session.title || selectedSession?.title || t('agent.untitled')}</span>
                <b>⌄</b>
              </button>
              {sessionMenuOpen && (
                <div className="agent-session-menu" role="dialog" aria-label={t('agent.recentSessions')}>
                  <header>
                    <span>{t('agent.recentSessions')}</span>
                    <small>{t('agent.sessionCount', { count: selectableSessions.length })}</small>
                  </header>
                  <div className="agent-session-menu__list">
                    {selectableSessions.map((session) => (
                      <button
                        type="button"
                        key={session.id}
                        className={session.id === sessionID ? 'active' : ''}
                        onClick={() => {
                          clearSessionPreparation(queryClient, projectID, episodeID, activeRuntimeKind);
                          setSelectedID(session.id);
                          setSessionMenuOpen(false);
                          setOptimisticInput(undefined);
                        }}
                      >
                        <i>{session.id === sessionID ? '✓' : ''}</i>
                        <span>
                          <strong>{session.title || t('agent.untitled')}</strong>
                          <small>
                            {session.status === 'active' ? t('agent.available') : t('agent.readOnlySession')}
                          </small>
                        </span>
                      </button>
                    ))}
                  </div>
                  <footer className="agent-session-menu__actions">
                    <button
                      type="button"
                      className="button button--secondary"
                      title={t('agent.compactContextHint')}
                      disabled={!canCompactContext}
                      onClick={organizeContext}
                    >
                      {t('agent.compactContext')}
                    </button>
                    {newSessionControls()}
                  </footer>
                </div>
              )}
            </div>
            {newSessionControls('header')}
          </div>
        ) : null}
      </header>
      <div className="agent-context">
        <div className="agent-context__heading">
          <span>{t('agent.currentContext')}</span>
          <strong>{contextLabel}</strong>
        </div>
        {contextTitle && <small title={contextTitle}>{contextTitle}</small>}
      </div>
      {(!runtimeReady || !newSessionModel) && (
        <div className="agent-runtime-error" role="status">
          <strong>
            {t(
              runtimeChecking
                ? 'agentAvailability.checking'
                : activeRuntimeKind === 'shotforward'
                  ? 'agentAvailability.cloud'
                  : 'agentAvailability.local',
            )}
          </strong>
          <p>{t('agentAvailability.setup')}</p>
          <Link to="/settings/account" className="button button--secondary">
            {t('agentAvailability.bind')}
          </Link>
          <Link to="/settings/agent" className="button button--secondary">
            {t('agentAvailability.configure')}
          </Link>
        </div>
      )}
      {sessionCreationError !== undefined && (
        <div className="agent-runtime-error" role="alert">
          <strong>{failureCopy.title}</strong>
          <p>
            {businessErrorMessage(businessErrorCode(sessionCreationError), t) ??
              (sessionCreationNetworkError ? failureCopy.networkHint : failureCopy.genericHint)}
          </p>
          <RechargeAction code={businessErrorCode(sessionCreationError)} />
          {sessionCreationRequestID && (
            <small>
              {failureCopy.diagnosticID}: {sessionCreationRequestID}
            </small>
          )}
          <Link to="/settings/runtime" className="button button--secondary">
            {failureCopy.openSettings}
          </Link>
        </div>
      )}
      {!sessionID ? (
        <div className="agent-empty">
          <div className="agent-orb">A</div>
          <h3>
            {preparing
              ? i18n.resolvedLanguage?.startsWith('zh')
                ? '正在准备新会话…'
                : 'Preparing your new conversation…'
              : t('agent.startTitle')}
          </h3>
          <p>
            {preparationScreen
              ? i18n.resolvedLanguage?.startsWith('zh')
                ? '可以先输入指令；准备完成后手动发送，不会发到旧会话。'
                : 'You can draft now and send when ready. Nothing will be sent to the previous conversation.'
              : availableModels.length > 0
                ? t('agent.startDescription')
                : t('agent.runtimeRequired')}
          </p>
          {(input || preparationScreen) && (
            <label className="agent-empty__draft">
              <span>
                {i18n.resolvedLanguage?.startsWith('zh')
                  ? '建议指令已准备好，可先修改；新建会话后不会自动发送。'
                  : 'The suggested instruction is ready to edit. Creating a session will not send it.'}
              </span>
              <textarea
                ref={inputRef}
                {...(preparationScreen
                  ? { 'aria-label': t('agent.placeholder'), placeholder: t('agent.placeholder') }
                  : {})}
                value={input}
                onChange={(event) => setInput(event.target.value)}
                rows={7}
              />
            </label>
          )}
          {newSessionControls('empty')}
          {preparationScreen && (
            <button type="button" className="send-button" disabled aria-label={t('agent.send')}>
              ↑
            </button>
          )}
        </div>
      ) : (
        <>
          <div className="agent-history" ref={historyRef} onScroll={observeHistoryScroll}>
            {conversation.hasNextPage && (
              <button
                type="button"
                className="agent-load-history"
                disabled={conversation.isFetchingNextPage}
                onClick={() => void loadEarlier()}
              >
                {conversation.isFetchingNextPage ? t('agent.loadingHistory') : t('agent.loadEarlier')}
              </button>
            )}
            {conversation.isPending && <div className="agent-history-note">{t('agent.loadingHistory')}</div>}
            {conversation.isError && !conversationHistoryInitializing && hasKnownConversation && (
              <div className="agent-history-note is-error">{t('agent.historyFailed')}</div>
            )}
            {!conversation.isPending &&
              !conversation.isError &&
              !hasKnownConversation &&
              !optimisticVisible &&
              !stream.assistantText && <div className="agent-history-note">{t('agent.noMessages')}</div>}
            {renderedConversationTurns.map(({ turn, messages, media = [] }) => (
              <div className="conversation-turn" key={turn.id}>
                {media.some((item) => item.origin !== 'generated') && (
                  <ConversationMediaStrip
                    projectID={projectID}
                    media={media.filter((item) => item.origin !== 'generated')}
                  />
                )}
                {messages.map((message) => (
                  <div
                    className={`conversation-message conversation-message--${message.role}${message.phase === 'commentary' ? ' is-commentary' : ''}`}
                    key={message.id}
                  >
                    {message.role === 'assistant' && <span>Agent</span>}
                    {message.role === 'assistant' ? (
                      <AgentMarkdown text={message.text} />
                    ) : (
                      <div>{message.text}</div>
                    )}
                  </div>
                ))}
                {media
                  .filter((item) => item.origin === 'generated')
                  .map((item) => (
                    <ConversationMediaResult
                      key={item.artifact_id}
                      projectID={projectID}
                      media={item}
                      copy={conversationMediaCopy}
                      continueDisabled={Boolean(sendingCurrent)}
                      onContinue={() => continueWithMedia(item)}
                      onSave={() => setMediaAction({ media: item })}
                    />
                  ))}
                {(changeSetsByTurn.get(turn.id) ?? []).map((item) => (
                  <ChangeSetCard
                    item={item}
                    key={item.id}
                    undoing={undo.isPending && undo.variables === item.id}
                    undoFailed={undo.isError && undo.variables === item.id}
                    onUndo={() => undo.mutate(item.id)}
                  />
                ))}
                {!isLiveTurnStatus(turn.status) && (
                  <TurnState
                    status={turn.status}
                    label={t(`agent.turn.${turn.status}`, { defaultValue: turn.status })}
                    detail={turn.failure?.summary}
                    failureCode={turn.failure?.code}
                  />
                )}
              </div>
            ))}
            {stream.assistantText && !liveReplyIsCanonical && optimisticFollowsStream && (
              <div className="conversation-message conversation-message--assistant is-streaming">
                <span>Agent</span>
                <AgentMarkdown text={stream.assistantText} />
              </div>
            )}
            {optimisticVisible && !optimisticAttachedToTurn && optimisticInput && (
              <div className="conversation-turn conversation-turn--optimistic">
                {optimisticInput.media.length > 0 && (
                  <ConversationMediaStrip projectID={projectID} media={optimisticInput.media} />
                )}
                <div className="conversation-message conversation-message--user is-pending">
                  <div>{optimisticInput.text}</div>
                </div>
              </div>
            )}
            {optimisticTerminalTurn && !optimisticConversationTurn && (
              <TurnState
                status={optimisticTerminalTurn.status}
                label={t(`agent.turn.${optimisticTerminalTurn.status}`, {
                  defaultValue: optimisticTerminalTurn.status,
                })}
                detail={optimisticTerminalTurn.failure?.summary}
                failureCode={optimisticTerminalTurn.failure?.code}
              />
            )}
            {stream.assistantText && !liveReplyIsCanonical && !optimisticFollowsStream && (
              <div className="conversation-message conversation-message--assistant is-streaming">
                <span>Agent</span>
                <AgentMarkdown text={stream.assistantText} />
              </div>
            )}
            {!liveTurnIsCanonical &&
              (changeSetsByTurn.get(stream.assistantTurnID) ?? []).map((item) => (
                <ChangeSetCard
                  item={item}
                  key={item.id}
                  undoing={undo.isPending && undo.variables === item.id}
                  undoFailed={undo.isError && undo.variables === item.id}
                  onUndo={() => undo.mutate(item.id)}
                />
              ))}
            {liveStatus && (
              <TurnState
                live
                status={liveStatus}
                label={t(`agent.turn.${liveStatus}`, { defaultValue: liveStatus })}
                detail={liveProgress || undefined}
              />
            )}
            {!followLatest && (
              <button type="button" className="agent-scroll-latest" onClick={scrollToLatest}>
                ↓ {t('agent.backToLatest')}
              </button>
            )}
          </div>
          {canSend ? (
            <form className="agent-composer" onSubmit={submit}>
              {contextCompacting && (
                <div role="status" className="agent-history-note">
                  {t('agent.contextCompacting')}
                </div>
              )}
              {compactionResultCurrent && compacting.isSuccess && !contextCompacting && (
                <div role="status" className="agent-history-note">
                  {t('agent.contextCompacted')}
                </div>
              )}
              {compactionResultCurrent && compacting.isError && (
                <div role="alert" className="form-error">
                  {t(contextCompactionErrorKey(compacting.error))}
                </div>
              )}
              {sending.isError && sending.variables?.isCurrentUI() && (
                <div role="alert" className="form-error">
                  {businessErrorMessage(businessErrorCode(sending.error), t) ?? t('businessErrors.unknown')}
                  <RechargeAction code={businessErrorCode(sending.error)} />
                </div>
              )}
              {businessErrorMessage(stream.toolFailureCode, t) && (
                <div role="alert" className="form-error">
                  {businessErrorMessage(stream.toolFailureCode, t)}
                  <RechargeAction code={stream.toolFailureCode} />
                </div>
              )}
              {attachments.length > 0 && (
                <div className="agent-composer-media">
                  {attachments.map((media) => (
                    <div key={media.artifact_id}>
                      <img src={artifactContentURL(projectID, media.artifact_id)} alt="" />
                      <span title={media.name}>{media.name}</span>
                      <button
                        type="button"
                        aria-label={conversationMediaCopy.remove}
                        onClick={() =>
                          updateAttachments((current) =>
                            current.filter((item) => item.artifact_id !== media.artifact_id),
                          )
                        }
                      >
                        ×
                      </button>
                    </div>
                  ))}
                </div>
              )}
              <textarea
                ref={inputRef}
                value={input}
                onPaste={pasteImages}
                title={conversationMediaCopy.pasteHint}
                onChange={(event) => {
                  const next = event.target.value;
                  setInput(next);
                  if (
                    attachments.length < 5 &&
                    event.target.selectionStart === next.length &&
                    /(^|\s)@$/.test(next)
                  ) {
                    setAssetPickerOpen(true);
                  }
                }}
                placeholder={t('agent.placeholder')}
                rows={1}
              />
              <footer>
                <div className="agent-composer__controls">
                  <input
                    ref={fileInputRef}
                    className="agent-media-file-input"
                    type="file"
                    accept=".png,.jpg,.jpeg,.webp,image/png,image/jpeg,image/webp"
                    multiple
                    onChange={(event) => void attachFiles(event)}
                  />
                  <button
                    type="button"
                    className="agent-composer-tool"
                    title={`${conversationMediaCopy.upload} · ${conversationMediaCopy.pasteHint}`}
                    aria-label={conversationMediaCopy.upload}
                    disabled={
                      Boolean(sendingCurrent) ||
                      attachments.length + pendingMediaCount >= maximumAgentAttachments
                    }
                    onClick={() => fileInputRef.current?.click()}
                  >
                    ＋
                  </button>
                  <div className="agent-asset-mention" ref={assetMentionRef}>
                    <button
                      type="button"
                      className="agent-composer-tool"
                      title={conversationMediaCopy.mentionReference}
                      aria-label={conversationMediaCopy.mentionReference}
                      disabled={Boolean(sendingCurrent)}
                      onClick={() => setAssetPickerOpen((open) => !open)}
                    >
                      @
                    </button>
                    {assetPickerOpen && (
                      <div className="agent-asset-mention__menu">
                        <strong>{conversationMediaCopy.chooseAsset}</strong>
                        {(assets.data?.assets ?? []).map((asset) => (
                          <button
                            type="button"
                            key={asset.asset_id}
                            disabled={Boolean(sendingCurrent)}
                            onClick={() => void attachAsset(asset)}
                          >
                            <span>{asset.identity.name}</span>
                            <small>{asset.identity.kind}</small>
                          </button>
                        ))}
                        <strong>{conversationMediaCopy.chooseUploadedMedia}</strong>
                        {(referenceMedia.data?.reference_media ?? []).map((item) => (
                          <button
                            type="button"
                            key={item.artifact.artifact_id}
                            disabled={Boolean(sendingCurrent)}
                            onClick={() => attachReferenceMedia(item)}
                          >
                            <span>{item.name}</span>
                            <small>
                              {item.artifact.kind === 'image'
                                ? conversationMediaCopy.uploadedImage
                                : item.artifact.kind === 'video'
                                  ? conversationMediaCopy.uploadedVideo
                                  : conversationMediaCopy.uploadedAudio}
                            </small>
                          </button>
                        ))}
                        {!assets.isPending &&
                          !referenceMedia.isPending &&
                          !(assets.data?.assets.length ?? 0) &&
                          !(referenceMedia.data?.reference_media.length ?? 0) && (
                            <small>{conversationMediaCopy.noReferences}</small>
                          )}
                        {referenceMedia.isError && (
                          <small>{conversationMediaCopy.referenceMediaLoadFailed}</small>
                        )}
                      </div>
                    )}
                  </div>
                  <span role={mediaError || interrupting.isError ? 'alert' : undefined}>
                    {mediaError ||
                      (interrupting.isError
                        ? t('agent.stopFailed')
                        : interrupting.isPending
                          ? t('agent.stopping')
                          : uploadingMedia
                            ? conversationMediaCopy.uploading
                            : sendingCurrent || preparing
                              ? t('agent.sending')
                              : '')}
                  </span>
                </div>
                {liveTurn && sessionID ? (
                  <button
                    type="button"
                    className="send-button send-button--stop"
                    disabled={interrupting.isPending}
                    aria-label={interrupting.isPending ? t('agent.stopping') : t('agent.stop')}
                    onClick={() => interrupting.mutate({ sessionID, turnID: liveTurn.id })}
                  >
                    <span className="send-button__stop-icon" aria-hidden="true" />
                  </button>
                ) : (
                  <button
                    className="send-button"
                    disabled={
                      !input.trim() ||
                      !selectedModel ||
                      sendingCurrent ||
                      preparing ||
                      contextCompacting ||
                      uploadingMedia
                    }
                    aria-label={t('agent.send')}
                  >
                    ↑
                  </button>
                )}
              </footer>
            </form>
          ) : (
            <div className="agent-read-only">
              <span>{t('agent.readOnlyHint')}</span>
              <button
                type="button"
                className="button button--primary"
                disabled={!canCreateSession}
                onClick={createNewSession}
              >
                {t('agent.newSession')}
              </button>
            </div>
          )}
          {mediaAction && (
            <ConversationMediaActionDialog
              action={mediaAction}
              projectID={projectID}
              episodeID={episodeID}
              assets={assets.data?.assets ?? []}
              copy={conversationMediaCopy}
              onClose={() => setMediaAction(undefined)}
              onCompleted={() => {
                setMediaAction(undefined);
                void Promise.all([
                  queryClient.invalidateQueries({ queryKey: ['assets', projectID, episodeID] }),
                  queryClient.invalidateQueries({ queryKey: ['storyboard', projectID, episodeID] }),
                ]);
              }}
            />
          )}
        </>
      )}
    </aside>
  );
}

type ConversationMediaCopy = { [Key in keyof (typeof mediaCopy)['en']]: string };

function ConversationMediaStrip({
  projectID,
  media,
}: {
  projectID: string;
  media: AgentConversationMedia[];
}) {
  return (
    <div className="conversation-media-strip">
      {media.map((item) => (
        <figure key={`${item.artifact_id}-${item.origin}`} title={item.name}>
          <img src={artifactContentURL(projectID, item.artifact_id)} alt={item.name} />
          <figcaption>{item.name}</figcaption>
        </figure>
      ))}
    </div>
  );
}

function ConversationMediaResult({
  projectID,
  media,
  copy,
  continueDisabled,
  onContinue,
  onSave,
}: {
  projectID: string;
  media: AgentConversationMedia;
  copy: ConversationMediaCopy;
  continueDisabled: boolean;
  onContinue: () => void;
  onSave: () => void;
}) {
  const url = artifactContentURL(projectID, media.artifact_id);
  return (
    <article className="conversation-media-result">
      <img src={url} alt={media.name} />
      <div>
        <button type="button" disabled={continueDisabled} onClick={onContinue}>
          {copy.continueEdit}
        </button>
        <button type="button" onClick={onSave}>
          {copy.saveAsset}
        </button>
        <button
          type="button"
          onClick={() => void saveDownload(url, media.original_filename || 'shotforward-agent-image.png')}
        >
          {copy.download}
        </button>
      </div>
    </article>
  );
}

function ConversationMediaActionDialog({
  action,
  projectID,
  episodeID,
  assets,
  copy,
  onClose,
  onCompleted,
}: {
  action: MediaAction;
  projectID: string;
  episodeID: string;
  assets: EpisodeAsset[];
  copy: ConversationMediaCopy;
  onClose: () => void;
  onCompleted: () => void;
}) {
  const [saveMode, setSaveMode] = useState<'new' | 'version'>('new');
  const [assetID, setAssetID] = useState('');
  const [assetName, setAssetName] = useState('');
  const [assetKind, setAssetKind] = useState<AssetKind>('prop');
  const [pending, setPending] = useState(false);
  const [error, setError] = useState('');
  const assetKindLabels: Record<AssetKind, string> = {
    character: copy.character,
    location: copy.location,
    prop: copy.prop,
    style: copy.style,
  };
  const assetKindIcons: Record<AssetKind, string> = {
    character: '人',
    location: '景',
    prop: '物',
    style: '风',
  };
  const assetKindOptions: Array<{
    value: AssetKind;
    label: string;
    description: string;
    icon: string;
  }> = [
    {
      value: 'character',
      label: copy.character,
      description: copy.characterHint,
      icon: assetKindIcons.character,
    },
    {
      value: 'location',
      label: copy.location,
      description: copy.locationHint,
      icon: assetKindIcons.location,
    },
    { value: 'prop', label: copy.prop, description: copy.propHint, icon: assetKindIcons.prop },
    { value: 'style', label: copy.style, description: copy.styleHint, icon: assetKindIcons.style },
  ];
  const existingAssetOptions = assets.map((asset) => ({
    value: asset.asset_id,
    label: asset.identity.name,
    description: assetKindLabels[asset.identity.kind],
    icon: assetKindIcons[asset.identity.kind],
  }));
  const mediaFormat = action.media.mime_type.split('/').at(-1)?.toUpperCase();
  const mediaMetadata = [
    action.media.width > 0 && action.media.height > 0 ? `${action.media.width} × ${action.media.height}` : '',
    mediaFormat,
  ]
    .filter(Boolean)
    .join(' · ');
  async function complete(event: FormEvent) {
    event.preventDefault();
    setError('');
    setPending(true);
    try {
      let slotID: string;
      if (saveMode === 'new') {
        if (!assetName.trim()) throw new Error(copy.enterAssetName);
        const created = await createAsset(projectID, episodeID, assetKind, assetName.trim(), '');
        slotID = created.asset.primary_image_slot_id;
      } else {
        const selected = assets.find((asset) => asset.asset_id === assetID);
        if (!selected) throw new Error(copy.chooseExistingAsset);
        slotID = selected.primary_image_slot_id;
      }
      const details = await getMediaSlot(projectID, slotID, true);
      await assignArtifact(projectID, slotID, action.media.artifact_id, details.slot.revision, true);
      onCompleted();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : copy.operationFailed);
    } finally {
      setPending(false);
    }
  }

  return (
    <Dialog open label={copy.saveAsset} onClose={onClose}>
      <form className="dialog-panel agent-media-action-dialog" onSubmit={(event) => void complete(event)}>
        <header className="dialog-header">
          <div>
            <p>{copy.assetLibrary}</p>
            <h2>{copy.saveAsset}</h2>
            <small>{copy.saveAssetDescription}</small>
          </div>
          <button className="icon-button" type="button" aria-label={copy.close} onClick={onClose}>
            ×
          </button>
        </header>
        <div className="agent-media-action-dialog__body">
          <section className="agent-media-action-preview">
            <div className="agent-media-action-preview__frame">
              <img src={artifactContentURL(projectID, action.media.artifact_id)} alt={action.media.name} />
            </div>
            <div>
              <strong>{action.media.name}</strong>
              {mediaMetadata && <small>{mediaMetadata}</small>}
            </div>
          </section>
          <section className="agent-media-action-form">
            <div className="agent-media-save-modes" role="group" aria-label={copy.saveMode}>
              <button
                type="button"
                className={saveMode === 'new' ? 'active' : ''}
                aria-pressed={saveMode === 'new'}
                onClick={() => {
                  setSaveMode('new');
                  setError('');
                }}
              >
                {copy.newAsset}
              </button>
              <button
                type="button"
                className={saveMode === 'version' ? 'active' : ''}
                aria-pressed={saveMode === 'version'}
                onClick={() => {
                  setSaveMode('version');
                  setError('');
                }}
              >
                {copy.newVersion}
              </button>
            </div>
            {saveMode === 'new' ? (
              <div className="agent-media-action-fields">
                <label>
                  <span>{copy.assetName}</span>
                  <input
                    value={assetName}
                    placeholder={copy.assetNamePlaceholder}
                    autoFocus
                    onChange={(event) => setAssetName(event.target.value)}
                  />
                </label>
                <label>
                  <span>{copy.assetKind}</span>
                  <Select<AssetKind>
                    className="agent-media-action-select"
                    value={assetKind}
                    options={assetKindOptions}
                    ariaLabel={copy.assetKind}
                    onChange={setAssetKind}
                  />
                </label>
              </div>
            ) : (
              <div className="agent-media-action-fields">
                <label>
                  <span>{copy.chooseExistingAsset}</span>
                  <Select<string>
                    className="agent-media-action-select"
                    value={assetID}
                    options={existingAssetOptions}
                    placeholder={assets.length ? copy.chooseExistingAssetPlaceholder : copy.noAssets}
                    ariaLabel={copy.chooseExistingAsset}
                    onChange={setAssetID}
                  />
                </label>
              </div>
            )}
            <p className="agent-media-action-hint">
              <span aria-hidden="true">i</span>
              {saveMode === 'new' ? copy.newAssetHint : copy.newVersionHint}
            </p>
            {error && (
              <p className="agent-media-action-error" role="alert">
                {error}
              </p>
            )}
          </section>
        </div>
        <footer className="dialog-footer">
          <button type="button" className="button button--secondary" onClick={onClose}>
            {copy.cancel}
          </button>
          <button className="button button--primary" disabled={pending}>
            {pending ? copy.saving : saveMode === 'new' ? copy.createAssetAction : copy.saveVersionAction}
          </button>
        </footer>
      </form>
    </Dialog>
  );
}

function mediaReference(media: AgentConversationMedia): AgentMediaReference {
  return { artifact_id: media.artifact_id, name: media.name, origin: media.origin };
}

function uniqueMedia(values: AgentConversationMedia[]): AgentConversationMedia[] {
  const result = new Map<string, AgentConversationMedia>();
  for (const value of values) result.set(value.artifact_id, value);
  return [...result.values()];
}

function conversationMediaFromReference(value: EpisodeReferenceMedia): AgentConversationMedia {
  return {
    artifact_id: value.artifact.artifact_id,
    name: value.name,
    origin: 'asset',
    mime_type: value.artifact.mime_type,
    original_filename: value.artifact.original_filename,
    byte_size: value.artifact.byte_size,
    width: value.artifact.width,
    height: value.artifact.height,
    created_at: value.artifact.created_at,
  };
}

const agentProgressCopy = {
  zh: {
    retrying: '连接中断，正在重试…',
    waitingForModel: '正在等待模型响应',
    elapsed: '已耗时',
  },
  en: {
    retrying: 'Connection interrupted. Retrying…',
    waitingForModel: 'Waiting for the model response',
    elapsed: 'Elapsed',
  },
} as const;

const mediaCopy = {
  zh: {
    upload: '上传图片到当前对话',
    pasteHint: '也可在输入框粘贴截图或复制的图像。',
    mentionReference: '引用资产或上传素材',
    chooseAsset: '选择资产',
    chooseUploadedMedia: '选择上传素材',
    uploadedImage: '上传图片',
    uploadedVideo: '上传视频',
    uploadedAudio: '上传音频',
    noReferences: '暂无可引用的资产或上传素材',
    referenceMediaLoadFailed: '上传素材暂时无法加载',
    uploading: '正在准备图片附件…',
    unsupportedType: '仅支持 PNG、JPEG 或 WebP 图片。',
    tooLarge: '每张图片不能超过 25 MiB。',
    invalidMedia: '无法识别这张图片，或图片数据不完整。请换一张 PNG、JPEG 或 WebP 图片。',
    unreadableMedia: '无法读取图片或保存附件，请检查文件及项目目录权限后重试。',
    importUnavailable: '图片处理组件暂不可用，请重启 ShotForward 后重试。',
    sessionExpired: '本地会话已失效，请重新打开当前会话后重试。',
    uploadFailed: '图片上传失败，草稿和已添加附件已保留。请稍后重试。',
    whileSubmitting: '消息正在提交，请完成后再添加图片。',
    requestID: '请求编号：',
    imageLimit: '每条消息最多附加 5 张图片（含正在准备的图片）。',
    assetHasNoImage: '这个资产还没有当前图片版本。',
    switchSessionFirst: '附件属于当前会话；请先切换或新建会话，再添加附件。',
    remove: '移除附件',
    continueEdit: '继续修改',
    saveAsset: '保存为资产',
    assetLibrary: '项目资产库',
    saveAssetDescription: '将生成结果保存为可复用的项目资产。',
    close: '关闭',
    download: '下载',
    newAsset: '创建新资产',
    newVersion: '新增版本',
    saveMode: '资产保存方式',
    assetName: '资产名称',
    assetNamePlaceholder: '例如：主角正面造型',
    assetKind: '资产类型',
    character: '角色',
    characterHint: '人物、服装与造型设定',
    location: '场景',
    locationHint: '空间、环境与地点设定',
    prop: '道具',
    propHint: '物件、载具与产品素材',
    style: '风格',
    styleHint: '画面语言与视觉参考',
    chooseExistingAsset: '选择已有资产',
    chooseExistingAssetPlaceholder: '选择要追加版本的资产',
    noAssets: '暂无可用资产',
    newAssetHint: '创建一个可独立选择的新资产。',
    newVersionHint: '设为同一资产的当前新版本；已使用的画面不会自动替换。',
    enterAssetName: '请输入资产名称。',
    operationFailed: '操作失败，请重试。',
    cancel: '取消',
    confirm: '确认',
    createAssetAction: '创建资产',
    saveVersionAction: '保存新版本',
    saving: '正在保存…',
  },
  en: {
    upload: 'Upload images to this conversation',
    pasteHint: 'You can also paste a screenshot or a copied image into the message box.',
    mentionReference: 'Reference an Asset or uploaded medium',
    chooseAsset: 'Choose Asset',
    chooseUploadedMedia: 'Choose uploaded media',
    uploadedImage: 'Uploaded image',
    uploadedVideo: 'Uploaded video',
    uploadedAudio: 'Uploaded audio',
    noReferences: 'No Assets or uploaded media available',
    referenceMediaLoadFailed: 'Uploaded media could not be loaded',
    uploading: 'Preparing image attachments…',
    unsupportedType: 'Use a PNG, JPEG, or WebP image.',
    tooLarge: 'Each image must be 25 MiB or smaller.',
    invalidMedia:
      'This image could not be recognized or is incomplete. Choose another PNG, JPEG, or WebP image.',
    unreadableMedia:
      'The image could not be read or saved. Check the file and project folder permissions, then try again.',
    importUnavailable: 'Image processing is unavailable. Restart ShotForward and try again.',
    sessionExpired: 'The local session has expired. Reopen the conversation and try again.',
    uploadFailed: 'Image upload failed. Your draft and attached images are kept. Try again.',
    whileSubmitting: 'Your message is being submitted. Add images after it finishes.',
    requestID: 'Request ID:',
    imageLimit: 'Attach up to 5 images per message, including pending images.',
    assetHasNoImage: 'This Asset has no current image version.',
    switchSessionFirst:
      'Attachments belong to the current session. Switch or create the session before attaching them.',
    remove: 'Remove attachment',
    continueEdit: 'Continue editing',
    saveAsset: 'Save as Asset',
    assetLibrary: 'Project Asset Library',
    saveAssetDescription: 'Save this result as a reusable project Asset.',
    close: 'Close',
    download: 'Download',
    newAsset: 'Create new Asset',
    newVersion: 'Add version',
    saveMode: 'Asset save mode',
    assetName: 'Asset name',
    assetNamePlaceholder: 'For example: hero front view',
    assetKind: 'Asset type',
    character: 'Character',
    characterHint: 'People, costumes, and character looks',
    location: 'Location',
    locationHint: 'Spaces, environments, and places',
    prop: 'Prop',
    propHint: 'Objects, vehicles, and products',
    style: 'Style',
    styleHint: 'Visual language and look references',
    chooseExistingAsset: 'Choose an existing Asset',
    chooseExistingAssetPlaceholder: 'Choose the Asset to update',
    noAssets: 'No Assets available',
    newAssetHint: 'Creates a new selectable Asset.',
    newVersionHint: 'Selects a new version of the same Asset. Existing uses stay unchanged.',
    enterAssetName: 'Enter an Asset name.',
    operationFailed: 'The operation failed. Try again.',
    cancel: 'Cancel',
    confirm: 'Confirm',
    createAssetAction: 'Create Asset',
    saveVersionAction: 'Save new version',
    saving: 'Saving…',
  },
} as const;

const agentSessionFailureCopy = {
  zh: {
    title: '无法新建 Codex 会话',
    networkHint:
      'Codex 当前可能无法访问网络或登录已失效。请检查本机 VPN；如果外网受限，可在 Codex Runtime 设置中填写本机代理并重启后重试。',
    genericHint: '会话创建失败，请检查 Codex Runtime 状态后重试。',
    diagnosticID: '诊断编号',
    openSettings: '打开 Codex Runtime 设置',
  },
  en: {
    title: 'Could not create a Codex session',
    networkHint:
      'Codex may be unable to reach the network or its sign-in may have expired. Check your local VPN. If internet access is restricted, configure a local proxy in Codex Runtime settings, restart, and retry.',
    genericHint: 'The session could not be created. Check Codex Runtime and try again.',
    diagnosticID: 'Diagnostic ID',
    openSettings: 'Open Codex Runtime settings',
  },
} as const;

function normalizeRuntimeModel(model: RuntimeModel): RuntimeModel {
  return { ...model, runtime_kind: model.runtime_kind || 'user_codex' };
}

function ChangeSetCard({
  item,
  undoing,
  undoFailed,
  onUndo,
}: {
  item: ChangeSetSummary;
  undoing: boolean;
  undoFailed: boolean;
  onUndo: () => void;
}) {
  const { t } = useTranslation();
  const canUndo = item.status === 'applied' && !item.reverts_change_set_id && !item.reverted_by_change_set_id;
  return (
    <article className={`changeset-card status-${item.status}`}>
      <Link to={`/change-sets/${item.id}`}>
        <strong>{item.title}</strong>
        <span>
          {t(`agent.changeSetStatus.${item.status}`, { defaultValue: item.status })} ·{' '}
          {t('agent.changeCount', { count: item.operation_count })}
        </span>
      </Link>
      {item.reverts_change_set_id && <small>{t('agent.undoRecord')}</small>}
      {item.reverted_by_change_set_id && <small>{t('agent.undone')}</small>}
      {undoFailed && (
        <small className="changeset-card__error" role="alert">
          {t('agent.undoFailed')}
        </small>
      )}
      {canUndo && (
        <button type="button" disabled={undoing} onClick={onUndo}>
          {undoing ? t('agent.undoing') : t('agent.undo')}
        </button>
      )}
    </article>
  );
}

function chronologicalTurns(pages: { turns: AgentConversationTurn[] }[]): AgentConversationTurn[] {
  const result = new Map<string, AgentConversationTurn>();
  for (const page of [...pages].reverse()) {
    for (const turn of page.turns) {
      const previous = result.get(turn.turn.id);
      if (!previous) {
        result.set(turn.turn.id, turn);
        continue;
      }
      // The newest page's order is authoritative once Runtime history catches
      // up. Add older-only messages around shared anchors instead of appending
      // newly received replies after all locally persisted steers.
      const messages = [...turn.messages];
      const findMessageIndex = (message: AgentConversationMessage) =>
        messages.findIndex(
          (item) =>
            item.id === message.id ||
            (message.client_operation_id && item.client_operation_id === message.client_operation_id),
        );
      for (const [index, message] of previous.messages.entries()) {
        if (findMessageIndex(message) >= 0) continue;
        const nextAnchor = previous.messages
          .slice(index + 1)
          .map(findMessageIndex)
          .find((value) => value >= 0);
        if (nextAnchor === undefined) messages.push(message);
        else messages.splice(nextAnchor, 0, message);
      }
      result.set(turn.turn.id, {
        ...turn,
        messages,
        media: uniqueMedia([...(previous.media ?? []), ...(turn.media ?? [])]),
      });
    }
  }
  return [...result.values()].sort((left, right) => compareTurns(left.turn, right.turn));
}

function compareTurns(left: { id: string; created_at: string }, right: { id: string; created_at: string }) {
  return left.created_at.localeCompare(right.created_at) || left.id.localeCompare(right.id);
}

function contextCompactionErrorKey(error: unknown) {
  if (error instanceof APIError) {
    if (
      error.code === 'context_compaction_unavailable' ||
      error.code === 'compaction_unavailable' ||
      error.status === 404
    )
      return 'agent.contextCompactionUnavailable';
    if (error.code === 'context_compaction_busy') return 'agent.contextCompactionBusy';
    if (error.code === 'context_compaction_timeout') return 'agent.contextCompactionTimeout';
  }
  return 'agent.contextCompactionFailed';
}

function isLiveTurnStatus(status: string) {
  return !['succeeded', 'completed', 'failed', 'interrupted'].includes(status);
}

function isConversationHistoryInitializing(error: unknown): boolean {
  return error instanceof APIError && error.code === 'history_not_ready';
}

function hasCanonicalInput(turns: AgentConversationTurn[], optimistic: OptimisticInput): boolean {
  // Until the submit response supplies the new turn ID, identical text may belong to
  // an older turn. Keep the optimistic bubble visible instead of deduplicating by text alone.
  if (!optimistic.turnID) return false;
  const users = turns
    .find(({ turn }) => turn.id === optimistic.turnID)
    ?.messages.filter((message) => message.role === 'user');
  if (!users) return false;
  if (users.some((message) => message.client_operation_id)) {
    return users.some((message) => message.client_operation_id === optimistic.clientOperationID);
  }
  // Older daemons do not supply operation IDs. Only a newly added occurrence
  // can replace this submission, not an earlier identical steer in the same Turn.
  return (
    users.filter((message) => message.text === optimistic.text).length >
    (optimistic.previousSameTextCounts[optimistic.turnID] ?? 0)
  );
}

function attachOptimisticInput(
  turns: AgentConversationTurn[],
  optimistic: OptimisticInput | undefined,
): AgentConversationTurn[] {
  if (!optimistic?.turnID || hasCanonicalInput(turns, optimistic)) return turns;
  return turns.map((value) => {
    if (value.turn.id !== optimistic.turnID) return value;
    const message = {
      id: `optimistic-user-${optimistic.clientOperationID}`,
      client_operation_id: optimistic.clientOperationID,
      role: 'user' as const,
      text: optimistic.text,
    };
    return {
      ...value,
      messages:
        optimistic.action === 'steer_turn' ? [...value.messages, message] : [message, ...value.messages],
    };
  });
}

function TurnState({
  status,
  label,
  detail,
  failureCode,
  live = false,
}: {
  status: string;
  label: string;
  detail?: string | undefined;
  failureCode?: string | undefined;
  live?: boolean;
}) {
  const { t } = useTranslation();
  const message = businessErrorMessage(failureCode, t) ?? detail;
  return (
    <div className={`turn-row${live ? ' is-live' : ''}`} role={live ? 'status' : undefined}>
      <span className={`turn-state state-${status}`} />
      <span>{label}</span>
      {message && <small>{message}</small>}
      <RechargeAction code={failureCode} />
    </div>
  );
}
