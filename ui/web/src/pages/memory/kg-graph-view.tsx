import { useRef, useMemo, useCallback, useState, useEffect } from "react";
import ForceGraph2D, { type ForceGraphMethods } from "react-force-graph-2d";
import { Maximize2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { useTranslation } from "react-i18next";
import { useUiStore } from "@/stores/use-ui-store";
import type { KGEntity, KGRelation } from "@/types/knowledge-graph";

const NODE_LIMIT_OPTIONS = [100, 200, 300, 500] as const;
const DEFAULT_NODE_LIMIT = 200;
const NODE_R = 5;
const DOUBLE_CLICK_MS = 280;

// Solid colors per entity type — used for both circle fill and glow
const TYPE_COLORS: Record<string, string> = {
  person: "#E85D24", organization: "#ef4444", project: "#22c55e",
  product: "#f97316", technology: "#3b82f6", task: "#f59e0b",
  event: "#ec4899", document: "#8b5cf6", concept: "#a78bfa", location: "#14b8a6",
};
const DEFAULT_COLOR = "#9ca3af";

function computeDegreeMap(entities: KGEntity[], relations: KGRelation[]): Map<string, number> {
  const deg = new Map<string, number>();
  const ids = new Set(entities.map((e) => e.id));
  for (const r of relations) {
    if (ids.has(r.source_entity_id)) deg.set(r.source_entity_id, (deg.get(r.source_entity_id) ?? 0) + 1);
    if (ids.has(r.target_entity_id)) deg.set(r.target_entity_id, (deg.get(r.target_entity_id) ?? 0) + 1);
  }
  return deg;
}

// --- Graph data types for react-force-graph ---
interface GraphNode {
  id: string;
  name: string;
  entityType: string;
  color: string;
  neighbors: Set<string>;
  linkIds: Set<string>;
  degree: number;
  // Force-graph adds these at runtime
  x?: number;
  y?: number;
}
interface GraphLink {
  id: string;
  source: string;
  target: string;
  label: string;
}

interface KGGraphViewProps {
  entities: KGEntity[];
  relations: KGRelation[];
  onEntityClick?: (entity: KGEntity) => void;
}

export function KGGraphView({ entities: allEntities, relations: allRelations, onEntityClick }: KGGraphViewProps) {
  const { t } = useTranslation("memory");
  const theme = useUiStore((s) => s.theme);
  const isDark = theme === "dark" || (theme === "system" && window.matchMedia("(prefers-color-scheme: dark)").matches);

  const graphRef = useRef<ForceGraphMethods<GraphNode, GraphLink>>(undefined);
  const containerRef = useRef<HTMLDivElement>(null);
  const [dimensions, setDimensions] = useState({ width: 600, height: 400 });
  const [selectedNodeId, setSelectedNodeId] = useState<string | null>(null);
  const [nodeLimit, setNodeLimit] = useState(DEFAULT_NODE_LIMIT);

  // Double-click detection refs
  const clickTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const lastClickRef = useRef<string | null>(null);

  // --- Container sizing via ResizeObserver ---
  useEffect(() => {
    const el = containerRef.current;
    if (!el) return;
    const ro = new ResizeObserver(([entry]) => {
      if (!entry) return;
      setDimensions({ width: entry.contentRect.width, height: entry.contentRect.height });
    });
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  // --- Limit entities by degree centrality ---
  const totalCount = allEntities.length;
  const isLimited = totalCount > nodeLimit;
  const entities = useMemo(() => {
    if (totalCount <= nodeLimit) return allEntities;
    const deg = computeDegreeMap(allEntities, allRelations);
    return [...allEntities].sort((a, b) => (deg.get(b.id) ?? 0) - (deg.get(a.id) ?? 0)).slice(0, nodeLimit);
  }, [allEntities, allRelations, totalCount, nodeLimit]);

  const entityMap = useMemo(() => new Map(entities.map((e) => [e.id, e])), [entities]);

  // --- Build graph data for react-force-graph ---
  const graphData = useMemo(() => {
    const entityIds = new Set(entities.map((e) => e.id));
    const degreeMap = computeDegreeMap(entities, allRelations);

    const nodes: GraphNode[] = entities.map((e) => ({
      id: e.id,
      name: e.name,
      entityType: e.entity_type,
      color: TYPE_COLORS[e.entity_type] ?? DEFAULT_COLOR,
      neighbors: new Set<string>(),
      linkIds: new Set<string>(),
      degree: degreeMap.get(e.id) ?? 0,
    }));

    const nodeMap = new Map(nodes.map((n) => [n.id, n]));

    const links: GraphLink[] = allRelations
      .filter((r) => entityIds.has(r.source_entity_id) && entityIds.has(r.target_entity_id))
      .map((r) => {
        nodeMap.get(r.source_entity_id)?.neighbors.add(r.target_entity_id);
        nodeMap.get(r.target_entity_id)?.neighbors.add(r.source_entity_id);
        nodeMap.get(r.source_entity_id)?.linkIds.add(r.id);
        nodeMap.get(r.target_entity_id)?.linkIds.add(r.id);
        return {
          id: r.id,
          source: r.source_entity_id,
          target: r.target_entity_id,
          label: r.relation_type.replace(/_/g, " "),
        };
      });

    return { nodes, links };
  }, [entities, allRelations]);

  // --- Highlight sets derived from selected node ---
  const { highlightNodes, highlightLinks } = useMemo(() => {
    if (!selectedNodeId) return { highlightNodes: new Set<string>(), highlightLinks: new Set<string>() };
    const node = graphData.nodes.find((n) => n.id === selectedNodeId);
    if (!node) return { highlightNodes: new Set<string>(), highlightLinks: new Set<string>() };
    return {
      highlightNodes: new Set([selectedNodeId, ...node.neighbors]),
      highlightLinks: new Set(node.linkIds),
    };
  }, [selectedNodeId, graphData.nodes]);

  // --- Node Canvas rendering: colored circle + name label ---
  const nodeCanvasObject = useCallback(
    (node: GraphNode, ctx: CanvasRenderingContext2D, globalScale: number) => {
      const x = node.x ?? 0;
      const y = node.y ?? 0;
      const isSelected = node.id === selectedNodeId;
      const isNeighbor = highlightNodes.has(node.id);
      const isDimmed = !!selectedNodeId && !isSelected && !isNeighbor;
      const r = NODE_R + (node.degree >= 4 ? 2 : 0);

      // Circle fill
      ctx.beginPath();
      ctx.arc(x, y, r, 0, 2 * Math.PI);
      ctx.fillStyle = isDimmed ? `${node.color}30` : node.color;
      ctx.fill();

      // Selection glow ring
      if (isSelected) {
        ctx.strokeStyle = node.color;
        ctx.lineWidth = 2.5 / globalScale;
        ctx.shadowColor = node.color;
        ctx.shadowBlur = 8 / globalScale;
        ctx.stroke();
        ctx.shadowBlur = 0;
      } else if (isNeighbor) {
        ctx.strokeStyle = node.color;
        ctx.lineWidth = 1.5 / globalScale;
        ctx.stroke();
      }

      // Name label — show when zoomed enough or highlighted
      if (globalScale > 0.5 || isSelected || isNeighbor) {
        const fontSize = Math.max(11 / globalScale, 2);
        ctx.font = `${isSelected ? "bold " : ""}${fontSize}px Inter, system-ui, sans-serif`;
        ctx.textAlign = "center";
        ctx.textBaseline = "top";
        ctx.fillStyle = isDimmed
          ? (isDark ? "rgba(255,255,255,0.12)" : "rgba(0,0,0,0.12)")
          : (isDark ? "#e2e8f0" : "#1e293b");
        ctx.fillText(node.name, x, y + r + 2 / globalScale);
      }
    },
    [selectedNodeId, highlightNodes, isDark],
  );

  // --- Link canvas overlay: draw label text only for highlighted links ---
  const linkCanvasObject = useCallback(
    (link: any, ctx: CanvasRenderingContext2D, globalScale: number) => {
      if (!highlightLinks.has(link.id)) return;
      const src = link.source;
      const tgt = link.target;
      if (!src?.x || !tgt?.x) return;
      const midX = (src.x + tgt.x) / 2;
      const midY = (src.y + tgt.y) / 2;
      const fontSize = Math.max(9 / globalScale, 2);
      ctx.font = `${fontSize}px Inter, system-ui, sans-serif`;
      ctx.textAlign = "center";
      ctx.textBaseline = "middle";
      // Background pill for readability
      const text = link.label || "";
      const metrics = ctx.measureText(text);
      const pad = 2 / globalScale;
      ctx.fillStyle = isDark ? "rgba(15,23,42,0.85)" : "rgba(255,255,255,0.9)";
      ctx.fillRect(midX - metrics.width / 2 - pad, midY - fontSize / 2 - pad, metrics.width + pad * 2, fontSize + pad * 2);
      // Text
      ctx.fillStyle = isDark ? "#94a3b8" : "#64748b";
      ctx.fillText(text, midX, midY);
    },
    [highlightLinks, isDark],
  );

  // --- Click handlers: single (highlight) vs double (open detail) ---
  const handleNodeClick = useCallback(
    (node: GraphNode) => {
      if (clickTimerRef.current && lastClickRef.current === node.id) {
        // Double click → open detail modal
        clearTimeout(clickTimerRef.current);
        clickTimerRef.current = null;
        lastClickRef.current = null;
        const entity = entityMap.get(node.id);
        if (entity && onEntityClick) onEntityClick(entity);
        return;
      }
      // Single click → highlight (after delay to distinguish from double)
      if (clickTimerRef.current) clearTimeout(clickTimerRef.current);
      lastClickRef.current = node.id;
      clickTimerRef.current = setTimeout(() => {
        setSelectedNodeId((prev) => (prev === node.id ? null : node.id));
        clickTimerRef.current = null;
      }, DOUBLE_CLICK_MS);
    },
    [entityMap, onEntityClick],
  );

  const handleBackgroundClick = useCallback(() => {
    setSelectedNodeId(null);
  }, []);

  const handleEngineStop = useCallback(() => {
    // No auto zoom — initial view from warmupTicks is already good
  }, []);

  // --- Empty state ---
  if (allEntities.length === 0) {
    return <div className="flex h-full items-center justify-center text-sm text-muted-foreground">{t("kg.graphView.empty")}</div>;
  }

  return (
    <div className="flex h-full flex-col rounded-md border overflow-hidden bg-background">
      <div ref={containerRef} className="min-h-0 flex-1 relative">
        <ForceGraph2D
          ref={graphRef}
          width={dimensions.width}
          height={dimensions.height}
          graphData={graphData}
          nodeId="id"
          nodeCanvasObject={nodeCanvasObject}
          nodeCanvasObjectMode={() => "replace"}
          nodePointerAreaPaint={(node: GraphNode, color, ctx) => {
            const r = NODE_R + (node.degree >= 4 ? 2 : 0) + 3;
            ctx.beginPath();
            ctx.arc(node.x ?? 0, node.y ?? 0, r, 0, 2 * Math.PI);
            ctx.fillStyle = color;
            ctx.fill();
          }}
          linkColor={(link: any) =>
            highlightLinks.has(link.id) ? "#64748b" : (isDark ? "#334155" : "#cbd5e1")
          }
          linkWidth={(link: any) => (highlightLinks.has(link.id) ? 2 : 0.5)}
          linkCanvasObject={linkCanvasObject}
          linkCanvasObjectMode={(link: any) => (highlightLinks.has(link.id) ? "after" : undefined)}
          linkDirectionalParticles={(link: any) => (highlightLinks.has(link.id) ? 2 : 0)}
          linkDirectionalParticleWidth={3}
          linkDirectionalParticleSpeed={0.004}
          onNodeClick={handleNodeClick}
          onBackgroundClick={handleBackgroundClick}
          onEngineStop={handleEngineStop}
          backgroundColor="transparent"
          d3AlphaDecay={0.04}
          d3VelocityDecay={0.3}
          warmupTicks={40}
          cooldownTime={4000}
          enableNodeDrag={true}
          minZoom={0.1}
          maxZoom={8}
        />
      </div>

      {/* Stats bar */}
      <div className="flex items-center gap-3 px-3 py-1.5 border-t text-[10px] text-muted-foreground">
        <span>{t("kg.graphView.nodes", { count: totalCount })}</span>
        <span>{t("kg.graphView.edges", { count: allRelations.length })}</span>
        {isLimited && (
          <span>· {t("kg.graphView.limitNote", { limit: nodeLimit, total: totalCount })}</span>
        )}
        <div className="flex-1" />
        <select
          value={nodeLimit}
          onChange={(e) => setNodeLimit(Number(e.target.value))}
          className="h-5 rounded border bg-background px-1 text-[10px]"
        >
          {NODE_LIMIT_OPTIONS.map((n) => (
            <option key={n} value={n}>{n} nodes</option>
          ))}
        </select>
        <Button variant="ghost" size="sm" className="h-6 px-1.5" onClick={() => graphRef.current?.zoomToFit(300, 20)}>
          <Maximize2 className="h-3 w-3" />
        </Button>
      </div>
    </div>
  );
}
