import {useEffect, useRef, useState} from 'react';
import {formatLocalISODate} from '../lib';

type MonthCursor = { year: number; month: number };
type MonthCell = { date: string; day: number; outside: boolean };

/** 把 YYYY-MM-DD 解析成月游标；非法或空值退回今天所在的月份。 */
function monthCursorOf(date: string): MonthCursor {
  const matched = /^(\d{4})-(\d{2})-\d{2}$/.exec(date || '');
  if (!matched) {
    const today = new Date();
    return { year: today.getFullYear(), month: today.getMonth() + 1 };
  }
  return { year: Number(matched[1]), month: Number(matched[2]) };
}

/** 按月平移游标，跨年时由 Date 自动进位或退位。 */
function shiftMonth(cursor: MonthCursor, delta: number): MonthCursor {
  const next = new Date(cursor.year, cursor.month - 1 + delta, 1);
  return { year: next.getFullYear(), month: next.getMonth() + 1 };
}

/** 月份标题固定为「YYYY 年 M 月」，两个面板的标题宽度才不会跳动。 */
function monthLabel(cursor: MonthCursor): string {
  return `${cursor.year} 年 ${cursor.month} 月`;
}

/**
 * 构造一个固定 6 行 × 7 列的月历，周一起始。
 * 前后会补上相邻月份的日期，和 Ant Design 的日期面板一致；补位日期仍可点击，
 * 因此不会出现点击边缘日期时选择丢失的空白格。
 */
function buildMonthCells(cursor: MonthCursor): MonthCell[] {
  const first = new Date(cursor.year, cursor.month - 1, 1);
  const mondayOffset = (first.getDay() + 6) % 7;
  const gridStart = new Date(cursor.year, cursor.month - 1, 1 - mondayOffset);
  const cells: MonthCell[] = [];
  for (let index = 0; index < 42; index += 1) {
    const date = new Date(gridStart);
    date.setDate(gridStart.getDate() + index);
    cells.push({
      date: formatLocalISODate(date),
      day: date.getDate(),
      outside: date.getMonth() !== cursor.month - 1,
    });
  }
  return cells;
}

/** 日期按钮的可读文案，屏幕阅读器念「2026-11-24」比只念「24」明确。 */
function dateAriaLabel(date: string, selected: boolean, today: boolean): string {
  return `${date}${today ? '，今天' : ''}${selected ? '，已选择' : ''}`;
}

/**
 * 用量统计的日期范围选择器。
 *
 * 交互照 Ant Design RangePicker：一个入口、点开后左右两个月、单击开始日期、
 * 再单击结束日期。首次点击只记草稿，第二次点击才提交给父组件，避免像原来两个
 * 原生 date input 那样每改一边就触发一次查询。
 */
export function UsageDateRangePicker({
  from,
  to,
  max,
  onChange,
}: {
  from: string;
  to: string;
  /** 可选的最后一天，用量统计传网关时区下的「今天」，未来日期会被禁用。 */
  max: string;
  onChange: (from: string, to: string) => void;
}) {
  const rootRef = useRef<HTMLDivElement | null>(null);
  const [open, setOpen] = useState(false);
  const [draftFrom, setDraftFrom] = useState(from || max);
  const [draftTo, setDraftTo] = useState(to);
  const [selectingEnd, setSelectingEnd] = useState(false);
  const [hoverDate, setHoverDate] = useState('');
  const [viewMonth, setViewMonth] = useState<MonthCursor>(() => shiftMonth(monthCursorOf(to || from || max), -1));

  const secondMonth = shiftMonth(viewMonth, 1);

  /** 每次打开都从当前已提交区间重新起稿，避免上次取消的草稿残留。 */
  function openPicker() {
    setDraftFrom(from || max);
    setDraftTo(to);
    setSelectingEnd(false);
    setHoverDate('');
    setViewMonth(shiftMonth(monthCursorOf(to || from || max), -1));
    setOpen(true);
  }

  /** 单击入口只负责开关；真正提交发生在第二个日期被选中时。 */
  function togglePicker() {
    if (open) {
      setOpen(false);
      return;
    }
    openPicker();
  }

  /** 选中一个端点：第一次记开始，第二次补齐结束并立即提交。 */
  function pickDate(date: string) {
    if (date > max) return;
    setHoverDate('');
    if (!selectingEnd) {
      setDraftFrom(date);
      setDraftTo('');
      setSelectingEnd(true);
      return;
    }
    const nextFrom = date < draftFrom ? date : draftFrom;
    const nextTo = date < draftFrom ? draftFrom : date;
    setDraftFrom(nextFrom);
    setDraftTo(nextTo);
    setSelectingEnd(false);
    setOpen(false);
    onChange(nextFrom, nextTo);
  }

  /** 「今天」是网关时区的今天，不是浏览器本地日期，避免跨时区选错日界。 */
  function pickToday() {
    setDraftFrom(max);
    setDraftTo(max);
    setSelectingEnd(false);
    setOpen(false);
    onChange(max, max);
  }

  useEffect(() => {
    if (!open) return undefined;
    const onPointerDown = (event: MouseEvent) => {
      const target = event.target as Node;
      if (rootRef.current?.contains(target)) return;
      setOpen(false);
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') return;
      event.preventDefault();
      event.stopPropagation();
      setOpen(false);
    };
    // 捕获阶段监听：工具栏外面可能还有弹窗自身的点击处理，冒泡阶段会漏掉。
    document.addEventListener('mousedown', onPointerDown, true);
    document.addEventListener('keydown', onKeyDown);
    return () => {
      document.removeEventListener('mousedown', onPointerDown, true);
      document.removeEventListener('keydown', onKeyDown);
    };
  }, [open]);

  // 选结束日期时，将鼠标悬停到的日期作为临时终点，先给用户看到完整范围。
  const previewRange = selectingEnd && hoverDate
    ? (hoverDate < draftFrom
      ? { start: hoverDate, end: draftFrom, preview: true }
      : { start: draftFrom, end: hoverDate, preview: true })
    : { start: draftFrom, end: selectingEnd ? '' : draftTo, preview: false };

  /** 渲染一个月份面板；补位日期也走同一套范围高亮。 */
  function renderMonth(cursor: MonthCursor, secondary = false) {
    return <section className={`usage-range-month${secondary ? ' is-secondary' : ''}`} aria-label={monthLabel(cursor)}>
      <div className="usage-range-weekdays" aria-hidden="true">
        {['一', '二', '三', '四', '五', '六', '日'].map((weekday) => <span key={weekday}>{weekday}</span>)}
      </div>
      <div className="usage-range-grid" onMouseLeave={() => setHoverDate('')}>
        {buildMonthCells(cursor).map((cell) => {
          const inRange = !!previewRange.start && !!previewRange.end && cell.date >= previewRange.start && cell.date <= previewRange.end;
          const isStart = cell.date === previewRange.start;
          const isEnd = cell.date === previewRange.end;
          const isToday = cell.date === max;
          const disabled = cell.date > max;
          const classes = [
            'usage-range-day',
            cell.outside ? 'is-outside' : '',
            inRange ? 'is-in-range' : '',
            isStart ? 'is-range-start' : '',
            isEnd ? 'is-range-end' : '',
            previewRange.preview ? 'is-preview' : '',
            isToday ? 'is-today' : '',
          ].filter(Boolean).join(' ');
          return <button
            key={cell.date}
            type="button"
            className={classes}
            disabled={disabled}
            aria-label={dateAriaLabel(cell.date, inRange, isToday)}
            aria-pressed={inRange}
            onMouseEnter={() => {
              if (selectingEnd) setHoverDate(cell.date);
            }}
            onClick={() => pickDate(cell.date)}
          ><span>{cell.day}</span></button>;
        })}
      </div>
    </section>;
  }

  const rangeText = from && to ? `${from} → ${to}` : '选择日期范围';
  return <div className="usage-date-range" ref={rootRef}>
    <button
      type="button"
      className={`usage-date-range-trigger${open ? ' open' : ''}`}
      aria-haspopup="dialog"
      aria-expanded={open}
      aria-label={`选择用量统计日期范围，当前为 ${rangeText}`}
      title="选择用量统计日期范围"
      onClick={togglePicker}
    >
      <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.9" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
        <rect x="3.5" y="5" width="17" height="15.5" rx="2.4" />
        <path d="M3.5 10h17M8 3v4M16 3v4" />
      </svg>
      <span className="usage-date-range-value">{from || '开始日期'}</span>
      <span className="usage-date-range-arrow" aria-hidden="true">→</span>
      <span className="usage-date-range-value">{to || '结束日期'}</span>
      <svg className="usage-date-range-caret" width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d="m6 10 6 6 6-6" /></svg>
    </button>

    {open ? <div className="usage-range-picker-pop" role="dialog" aria-label="选择用量统计日期范围">
      <div className="usage-range-nav">
        <button type="button" className="usage-range-nav-btn" aria-label="上个月" onClick={() => setViewMonth((current) => shiftMonth(current, -1))}>
          <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d="m15 18-6-6 6-6" /></svg>
        </button>
        <div className="usage-range-month-titles">
          <span>{monthLabel(viewMonth)}</span>
          <span className="is-secondary">{monthLabel(secondMonth)}</span>
        </div>
        <button type="button" className="usage-range-nav-btn" aria-label="下个月" onClick={() => setViewMonth((current) => shiftMonth(current, 1))}>
          <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d="m9 18 6-6-6-6" /></svg>
        </button>
      </div>
      <div className="usage-range-calendars">
        {renderMonth(viewMonth)}
        {renderMonth(secondMonth, true)}
      </div>
      <div className="usage-range-footer">
        <span aria-live="polite">
          {selectingEnd
            ? `开始日期 ${draftFrom}，请选择结束日期`
            : draftFrom && draftTo
              ? `已选择 ${draftFrom} → ${draftTo}`
              : '请选择开始日期'}
        </span>
        <div className="usage-range-footer-actions">
          <button type="button" className="mini-btn" onClick={pickToday}>今天</button>
          <button type="button" className="mini-btn" onClick={() => setOpen(false)}>取消</button>
        </div>
      </div>
    </div> : null}
  </div>;
}
