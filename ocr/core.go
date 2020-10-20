package ocr

import (
	"image"
	"math"
	"paddleocr-go/paddle"
	"sort"

	"github.com/LKKlein/gocv"
)

type PaddleModel struct {
	predictor *paddle.Predictor
	input     *paddle.ZeroCopyTensor
	outputs   []*paddle.ZeroCopyTensor

	useGPU     bool
	deviceID   int
	initGPUMem int
	numThreads int
	useMKLDNN  bool
}

func NewPaddleModel(args map[string]interface{}) *PaddleModel {
	return &PaddleModel{
		useGPU:     getBool(args, "use_gpu", false),
		deviceID:   getInt(args, "gpu_id", 0),
		initGPUMem: getInt(args, "gpu_mem", 1000),
		numThreads: getInt(args, "num_threads", 6),
		useMKLDNN:  getBool(args, "use_mkldnn", false),
	}
}

func (model *PaddleModel) LoadModel(modelDir string) {
	config := paddle.NewAnalysisConfig()
	config.SetModel(modelDir+"/model", modelDir+"/params")
	if model.useGPU {
		config.EnableUseGpu(model.initGPUMem, model.deviceID)
	} else {
		config.DisableGpu()
		config.SetCpuMathLibraryNumThreads(model.numThreads)
		if model.useMKLDNN {
			config.EnableMkldnn()
		}
	}

	config.EnableMemoryOptim()
	config.DisableGlogInfo()
	config.SwitchIrOptim(true)

	// false for zero copy tensor
	config.SwitchUseFeedFetchOps(false)
	config.SwitchSpecifyInputNames(true)

	model.predictor = paddle.NewPredictor(config)
	model.input = model.predictor.GetInputTensors()[0]
	model.outputs = model.predictor.GetOutputTensors()
}

type OCRText struct {
	bbox  [][]int
	text  string
	score float64
}

type TextPredictSystem struct {
	detector *DBDetector
	cls      *TextClassifier
	rec      *TextRecognizer
}

func NewTextPredictSystem(args map[string]interface{}) *TextPredictSystem {
	sys := &TextPredictSystem{
		detector: NewDBDetector(getString(args, "det_model_dir", ""), args),
		rec:      NewTextRecognizer(getString(args, "rec_model_dir", ""), args),
	}
	if getBool(args, "use_angle_cls", false) {
		sys.cls = NewTextClassifier(getString(args, "cls_model_dir", ""), args)
	}
	return sys
}

func (sys *TextPredictSystem) sortBoxes(boxes [][][]int) [][][]int {
	sort.Slice(boxes, func(i, j int) bool {
		if boxes[i][0][1] < boxes[j][0][1] {
			return true
		}
		if boxes[i][0][1] > boxes[j][0][1] {
			return false
		}
		return boxes[i][0][0] < boxes[j][0][0]
	})

	for i := 0; i < len(boxes)-1; i++ {
		if math.Abs(float64(boxes[i+1][0][1]-boxes[i][0][1])) < 10 && boxes[i+1][0][0] < boxes[i][0][0] {
			boxes[i], boxes[i+1] = boxes[i+1], boxes[i]
		}
	}
	return boxes
}

func (sys *TextPredictSystem) getRotateCropImage(img gocv.Mat, box [][]int) gocv.Mat {
	boxX := []int{box[0][0], box[1][0], box[2][0], box[3][0]}
	boxY := []int{box[0][1], box[1][1], box[2][1], box[3][1]}

	left, right, top, bottom := mini(boxX), maxi(boxX), mini(boxY), maxi(boxY)
	cropimg := img.Region(image.Rect(left, top, right, bottom))
	for i := 0; i < len(box); i++ {
		box[i][0] -= left
		box[i][1] -= top
	}

	cropW := int(math.Sqrt(math.Pow(float64(box[0][0]-box[1][0]), 2) + math.Pow(float64(box[0][1]-box[1][1]), 2)))
	cropH := int(math.Sqrt(math.Pow(float64(box[0][0]-box[3][0]), 2) + math.Pow(float64(box[0][1]-box[3][1]), 2)))
	ptsstd := make([]image.Point, 4)
	ptsstd[0] = image.Pt(0, 0)
	ptsstd[1] = image.Pt(cropW, 0)
	ptsstd[2] = image.Pt(cropW, cropH)
	ptsstd[3] = image.Pt(0, cropH)

	points := make([]image.Point, 4)
	points[0] = image.Pt(box[0][0], box[0][1])
	points[1] = image.Pt(box[1][0], box[1][1])
	points[2] = image.Pt(box[2][0], box[2][1])
	points[3] = image.Pt(box[3][0], box[3][1])

	M := gocv.GetPerspectiveTransform(points, ptsstd)
	defer M.Close()
	dstimg := gocv.NewMat()
	gocv.WarpPerspective(cropimg, &dstimg, M, image.Pt(cropW, cropH))

	if float64(dstimg.Rows()) >= float64(dstimg.Cols())*1.5 {
		srcCopy := gocv.NewMat()
		gocv.Transpose(dstimg, &srcCopy)
		defer dstimg.Close()
		gocv.Flip(srcCopy, &srcCopy, 0)
		return srcCopy
	}
	return dstimg
}

func (sys *TextPredictSystem) Run(img gocv.Mat) []OCRText {
	result := make([]OCRText, 0, 10)

	srcimg := gocv.NewMat()
	img.CopyTo(&srcimg)
	boxes := sys.detector.Run(img)
	if len(boxes) == 0 {
		return result
	}

	boxes = sys.sortBoxes(boxes)
	cropimages := make([]gocv.Mat, len(boxes))
	for i := 0; i < len(boxes); i++ {
		tmpbox := make([][]int, len(boxes[i]))
		copy(tmpbox, boxes[i])
		cropimg := sys.getRotateCropImage(srcimg, tmpbox)
		cropimages[i] = cropimg
	}
	if sys.cls != nil {
		cropimages = sys.cls.Run(cropimages)
	}
	recResult := sys.rec.Run(cropimages)
	return recResult
}